// Package base provides the shared runtime struct and core execution methods used by
// both the local and temporal runtime backends. It has no dependency on any backend-specific
// SDK (no Temporal, no workflow/activity imports).
package base

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/hooks"
	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	"github.com/google/uuid"
)

// Runtime holds the execution inputs shared by all runtime backends.
// Local and Temporal runtimes embed this struct and call its methods directly.
type Runtime struct {
	AgentSpec   runtime.AgentSpec
	AgentConfig runtime.AgentConfig
	Tracer      interfaces.Tracer
	Metrics     interfaces.Metrics
	// ToolExecutionMode controls whether tool calls in one LLM round are executed
	// in parallel or sequentially. Defaults to parallel when empty.
	ToolExecutionMode types.AgentToolExecutionMode
}

type ExecuteLLMInput struct {
	Logger           logger.Logger
	AgentName        string
	MessageID        string
	RunID            string
	Iteration        int
	Messages         []interfaces.Message
	SkipTools        bool
	MemoryContext    string
	RetrieverContext string
	Tools            []interfaces.Tool
	Emit             func(events.AgentEvent)
}

// BuildLLMRequest constructs an LLMRequest from the given messages and options.
// SystemMessage is the static system prompt only. When memoryContext or retrieverContext
// is non-empty each is prepended as a labeled user message (memory then RAG) before the
// conversation messages. Message content is sanitized for the LLM copy only; the caller's
// slice is not modified. tools is the per-run resolved tool list from [runtime.ExecuteRequest]
// or activity resolve.
func (rt *Runtime) BuildLLMRequest(messages []interfaces.Message, skipTools bool, memoryContext, retrieverContext string, tools []interfaces.Tool) *interfaces.LLMRequest {
	sanitized := sanitizeMessages(messages)
	outMsgs := make([]interfaces.Message, 0, len(sanitized)+2)
	if memoryContext != "" {
		outMsgs = append(outMsgs, interfaces.Message{
			Role:    interfaces.MessageRoleUser,
			Content: "Relevant Memories:\n" + memoryContext,
		})
	}
	if retrieverContext != "" {
		outMsgs = append(outMsgs, interfaces.Message{
			Role:    interfaces.MessageRoleUser,
			Content: "Relevant Context:\n" + retrieverContext,
		})
	}
	outMsgs = append(outMsgs, sanitized...)

	req := &interfaces.LLMRequest{
		SystemMessage:  rt.AgentSpec.SystemPrompt,
		ResponseFormat: rt.AgentSpec.ResponseFormat,
		Messages:       outMsgs,
	}
	ApplyLLMSampling(rt.AgentConfig.LLM.Sampling, req)
	if skipTools {
		req.Tools = []interfaces.ToolSpec{}
	} else {
		req.Tools = interfaces.ToolsToSpecs(tools)
	}
	return req
}

// RequiresApproval reports whether t requires human approval before execution.
// When no approval policy is configured the tool's own ApprovalRequired flag is used.
func (rt *Runtime) RequiresApproval(t interfaces.Tool) bool {
	if rt.AgentConfig.ToolApprovalPolicy == nil {
		if ar, ok := t.(interfaces.ToolApproval); ok && ar.ApprovalRequired() {
			return true
		}
		return false
	}
	return rt.AgentConfig.ToolApprovalPolicy.RequiresApproval(t)
}

// FetchConversationMessages loads prior messages from the conversation store.
// Returns an error when no conversation is configured or the store call fails.
func (rt *Runtime) FetchConversationMessages(ctx context.Context, log logger.Logger, conversationID string) ([]interfaces.Message, error) {
	log.Debug(ctx, "runtime: loading conversation history", slog.String("scope", "runtime"), slog.String("conversationID", conversationID))

	if rt.AgentConfig.Session.Conversation == nil {
		return nil, fmt.Errorf("conversation is not configured")
	}

	limit := rt.AgentConfig.Session.ConversationSize
	if limit <= 0 {
		limit = 20
	}

	ctx, sp := rt.Tracer.StartSpan(ctx, "conversation.get_messages",
		interfaces.Attribute{Key: "conversation.id", Value: conversationID},
		interfaces.Attribute{Key: "limit", Value: limit},
	)
	defer sp.End()

	messages, err := rt.AgentConfig.Session.Conversation.ListMessages(ctx, conversationID, interfaces.WithLimit(limit))
	if err != nil {
		sp.RecordError(err)
		return nil, fmt.Errorf("failed to list conversation messages: %w", err)
	}

	sp.SetAttribute("message.count", len(messages))
	log.Debug(ctx, "runtime: conversation history loaded", slog.String("scope", "runtime"), slog.Int("messageCount", len(messages)))
	return messages, nil
}

// llmResponseToResult converts an LLMResponse into an LLMResult, resolving tool metadata
// (display name, approval flag) from the registered tools list.
func (rt *Runtime) llmResponseToResult(resp *interfaces.LLMResponse, tools []interfaces.Tool) (*LLMResult, error) {
	result := &LLMResult{Content: resp.Content, Usage: CloneLLMUsage(resp.Usage)}
	for _, tc := range resp.ToolCalls {
		if tc == nil {
			continue
		}
		tool, ok := FindToolByName(tools, tc.ToolName)
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.ToolName)
		}
		displayName := tool.DisplayName()
		if displayName == "" {
			displayName = tc.ToolName
		}
		result.ToolCalls = append(result.ToolCalls, ToolCallRequest{
			ToolCallID:      tc.ToolCallID,
			ToolName:        tc.ToolName,
			ToolDisplayName: displayName,
			ToolKind:        types.KindOf(tool),
			Args:            tc.Args,
			NeedsApproval:   rt.RequiresApproval(tool),
		})
	}
	return result, nil
}

// emitEvent calls fn safely; a nil fn is a no-op.
func emitEvent(fn func(events.AgentEvent), ev events.AgentEvent) {
	if fn != nil {
		fn(ev)
	}
}

// ExecuteLLM calls the LLM in non-streaming mode, records metrics and traces, emits
// TEXT_MESSAGE_START / TEXT_MESSAGE_CONTENT / TEXT_MESSAGE_END events, and returns LLMResult.
// messageID and agentName are used only for event construction; emit may be nil.
func (rt *Runtime) ExecuteLLM(ctx context.Context, input ExecuteLLMInput) (*LLMResult, error) {
	req := rt.BuildLLMRequest(input.Messages, input.SkipTools, input.MemoryContext, input.RetrieverContext, input.Tools)
	if err := rt.runBeforeLLMRequest(ctx, input, req); err != nil {
		return nil, err
	}

	llmClient := rt.AgentConfig.LLM.Client
	model := llmClient.GetModel()
	provider := string(llmClient.GetProvider())
	modelAttr := interfaces.Attribute{Key: types.MetricAttrModel, Value: model}
	providerAttr := interfaces.Attribute{Key: types.MetricAttrProvider, Value: provider}

	input.Logger.Debug(ctx, "runtime: LLM generate started", slog.String("scope", "runtime"), slog.Int("messageCount", len(input.Messages)))

	rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallStarted, modelAttr, providerAttr)
	llmStart := time.Now()

	ctx, sp := rt.Tracer.StartSpan(ctx, "llm.generate",
		interfaces.Attribute{Key: "agent.name", Value: strings.TrimSpace(input.AgentName)},
		interfaces.Attribute{Key: "message.count", Value: len(input.Messages)},
		modelAttr,
		providerAttr,
	)
	resp, err := llmClient.Generate(ctx, req)
	llmLatency := float64(time.Since(llmStart).Milliseconds())
	if err != nil {
		sp.RecordError(err)
		sp.End()
		rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
		return nil, err
	}
	sp.End()

	if err := rt.runAfterLLMResponse(ctx, input, resp); err != nil {
		return nil, err
	}

	rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
	rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallCompleted, modelAttr, providerAttr)
	if resp.Usage != nil {
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMTokensInput, float64(resp.Usage.PromptTokens), modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMTokensOutput, float64(resp.Usage.CompletionTokens), modelAttr, providerAttr)
	}

	input.Logger.Debug(ctx, "runtime: LLM generate completed", slog.String("scope", "runtime"), slog.Int("messageCount", len(input.Messages)))

	result, err := rt.llmResponseToResult(resp, input.Tools)
	if err != nil {
		return nil, err
	}

	emitEvent(input.Emit, events.NewAgentTextMessageStartEvent(input.MessageID, string(interfaces.MessageRoleAssistant)))
	emitEvent(input.Emit, events.NewAgentTextMessageContentEvent(input.MessageID, result.Content))
	emitEvent(input.Emit, events.NewAgentTextMessageEndEvent(input.MessageID))
	return result, nil
}

// ExecuteLLMStream calls the LLM in streaming mode. When the LLM client does not support streaming
// it falls back to Generate automatically. Delta events (text content, reasoning) are emitted via
// emit as chunks arrive; a final TEXT_MESSAGE_START/CONTENT/END triple is emitted for non-streaming
// fallback. emit may be nil.
func (rt *Runtime) ExecuteLLMStream(ctx context.Context, input ExecuteLLMInput) (*LLMResult, error) {
	req := rt.BuildLLMRequest(input.Messages, input.SkipTools, input.MemoryContext, input.RetrieverContext, input.Tools)
	if err := rt.runBeforeLLMRequest(ctx, input, req); err != nil {
		return nil, err
	}

	llmClient := rt.AgentConfig.LLM.Client
	model := llmClient.GetModel()
	provider := string(llmClient.GetProvider())
	modelAttr := interfaces.Attribute{Key: types.MetricAttrModel, Value: model}
	providerAttr := interfaces.Attribute{Key: types.MetricAttrProvider, Value: provider}
	isStreamSupported := llmClient.IsStreamSupported()

	rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallStarted, modelAttr, providerAttr)
	llmStart := time.Now()

	ctx, sp := rt.Tracer.StartSpan(ctx, "llm.stream",
		interfaces.Attribute{Key: "agent.name", Value: strings.TrimSpace(input.AgentName)},
		interfaces.Attribute{Key: "message.count", Value: len(input.Messages)},
		interfaces.Attribute{Key: "streaming", Value: isStreamSupported},
		modelAttr,
		providerAttr,
	)
	defer sp.End()

	// Helpers to track open/close state for text message and reasoning events.
	textMsgOpen := false
	openTextMsg := func() {
		if textMsgOpen {
			return
		}
		emitEvent(input.Emit, events.NewAgentTextMessageStartEvent(input.MessageID, string(interfaces.MessageRoleAssistant)))
		textMsgOpen = true
	}
	closeTextMsg := func() {
		if !textMsgOpen {
			return
		}
		emitEvent(input.Emit, events.NewAgentTextMessageEndEvent(input.MessageID))
		textMsgOpen = false
	}
	// If the model never sent text chunks still emit one assistant turn (empty for tool-only).
	finalizeAssistantText := func(result *LLMResult) {
		if textMsgOpen {
			closeTextMsg()
			return
		}
		openTextMsg()
		emitEvent(input.Emit, events.NewAgentTextMessageContentEvent(input.MessageID, result.Content))
		closeTextMsg()
	}

	// Non-streaming fallback: use Generate and emit a complete text message.
	if !isStreamSupported {
		input.Logger.Debug(ctx, "runtime: LLM stream unsupported, using generate", slog.String("scope", "runtime"))
		resp, err := llmClient.Generate(ctx, req)
		llmLatency := float64(time.Since(llmStart).Milliseconds())
		if err != nil {
			sp.RecordError(err)
			rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
			rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
			return nil, err
		}
		if err := rt.runAfterLLMResponse(ctx, input, resp); err != nil {
			sp.RecordError(err)
			rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
			rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
			return nil, err
		}
		result, err := rt.llmResponseToResult(resp, input.Tools)
		if err != nil {
			sp.RecordError(err)
			rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
			rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
			return nil, err
		}
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
		rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallCompleted, modelAttr, providerAttr)
		if resp.Usage != nil {
			rt.Metrics.RecordHistogram(ctx, types.MetricLLMTokensInput, float64(resp.Usage.PromptTokens), modelAttr, providerAttr)
			rt.Metrics.RecordHistogram(ctx, types.MetricLLMTokensOutput, float64(resp.Usage.CompletionTokens), modelAttr, providerAttr)
		}
		finalizeAssistantText(result)
		return result, nil
	}

	stream, err := llmClient.GenerateStream(ctx, req)
	if err != nil {
		sp.RecordError(err)
		rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, float64(time.Since(llmStart).Milliseconds()), modelAttr, providerAttr)
		return nil, err
	}

	// Reasoning AG-UI order: REASONING_START → REASONING_MESSAGE_START → REASONING_MESSAGE_CONTENT* →
	// REASONING_MESSAGE_END → REASONING_END (flushed before the first assistant text delta, or at stream end).
	var reasoningMID string
	reasoningPhaseOpen := false
	reasoningMsgOpen := false
	flushReasoning := func() {
		if reasoningMsgOpen {
			emitEvent(input.Emit, events.NewAgentReasoningMessageEndEvent(reasoningMID))
			reasoningMsgOpen = false
		}
		if reasoningPhaseOpen {
			emitEvent(input.Emit, events.NewAgentReasoningEndEvent(reasoningMID))
			reasoningPhaseOpen = false
		}
	}
	openReasoning := func() {
		if reasoningPhaseOpen {
			return
		}
		reasoningMID = uuid.New().String()
		emitEvent(input.Emit, events.NewAgentReasoningStartEvent(reasoningMID))
		reasoningPhaseOpen = true
		emitEvent(input.Emit, events.NewAgentReasoningMessageStartEvent(reasoningMID, string(interfaces.MessageRoleReasoning)))
		reasoningMsgOpen = true
	}

	for stream.Next() {
		chunk := stream.Current()
		if chunk == nil {
			continue
		}
		if chunk.ContentDelta != "" {
			flushReasoning()
			openTextMsg()
			emitEvent(input.Emit, events.NewAgentTextMessageContentEvent(input.MessageID, chunk.ContentDelta))
		}
		if chunk.ThinkingDelta != "" {
			openReasoning()
			emitEvent(input.Emit, events.NewAgentReasoningMessageContentEvent(reasoningMID, chunk.ThinkingDelta))
		}
	}
	flushReasoning()

	llmLatency := float64(time.Since(llmStart).Milliseconds())
	if err := stream.Err(); err != nil {
		sp.RecordError(err)
		rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
		return nil, err
	}

	resp := stream.GetResult()
	if resp == nil {
		err := fmt.Errorf("stream completed without result")
		sp.RecordError(err)
		rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
		return nil, err
	}

	if err := rt.runAfterLLMResponse(ctx, input, resp); err != nil {
		sp.RecordError(err)
		rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
		return nil, err
	}

	result, err := rt.llmResponseToResult(resp, input.Tools)
	if err != nil {
		sp.RecordError(err)
		rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallFailed, modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
		return nil, err
	}

	rt.Metrics.RecordHistogram(ctx, types.MetricLLMLatencyMs, llmLatency, modelAttr, providerAttr)
	rt.Metrics.IncrementCounter(ctx, types.MetricLLMCallCompleted, modelAttr, providerAttr)
	if resp.Usage != nil {
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMTokensInput, float64(resp.Usage.PromptTokens), modelAttr, providerAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricLLMTokensOutput, float64(resp.Usage.CompletionTokens), modelAttr, providerAttr)
	}

	input.Logger.Debug(ctx, "runtime: LLM stream completed", slog.String("scope", "runtime"))
	finalizeAssistantText(result)
	return result, nil
}

// ExecuteTool runs a tool with optional memory scope; save_memory on on-demand store routes to [StoreMemoryRecords].
// Retriever tools use [Runtime.executeRetrieverTool] inside [Runtime.executeTool].
// For [types.ToolKindNative] and [types.ToolKindMCP] tools, [hooks.BeforeToolHook] and
func (rt *Runtime) ExecuteTool(ctx context.Context, input ExecuteToolInput, memScope interfaces.MemoryScope) (string, error) {
	if input.ToolName == types.SaveMemoryToolName && rt.MemoryStoreOnDemand() {
		return rt.executeSaveMemoryTool(ctx, input, memScope)
	}
	return rt.executeTool(ctx, input)
}

// executeTool finds the named tool and executes it, recording tracing and metrics.
// Returns the string representation of the tool result.
func (rt *Runtime) executeTool(ctx context.Context, input ExecuteToolInput) (string, error) {
	log := input.Logger
	toolName := input.ToolName
	args := input.Args

	log.Debug(ctx, "runtime: tool execute started", slog.String("scope", "runtime"), slog.String("tool", toolName), slog.Int("argCount", len(args)))

	tool, ok := FindToolByName(input.Tools, toolName)
	if !ok {
		log.Warn(ctx, "runtime: unknown tool", slog.String("scope", "runtime"), slog.String("tool", toolName))
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}

	kind := types.KindOf(tool)
	runHooks := len(rt.AgentConfig.Hooks) > 0 && kind.HooksEligible()

	var hookedCall hooks.ToolCall
	if runHooks {
		var err error
		hookedCall, err = rt.runBeforeToolHooks(ctx, input, tool)
		if err != nil {
			return "", err
		}
		args = hookedCall.Args
	}

	toolAttr := interfaces.Attribute{Key: types.MetricAttrTool, Value: toolName}
	rt.Metrics.IncrementCounter(ctx, types.MetricToolCallStarted, toolAttr)
	toolStart := time.Now()

	ctx, sp := rt.Tracer.StartSpan(ctx, "tool.execute",
		interfaces.Attribute{Key: "tool.name", Value: toolName},
		interfaces.Attribute{Key: "arg.count", Value: len(args)},
	)
	defer sp.End()

	var content string
	var execErr error
	switch kind {
	case types.ToolKindRetriever:
		content, execErr = rt.executeRetrieverTool(ctx, input, tool, args)
	default:
		var result any
		execCtx := interfaces.WithToolExecMeta(ctx, interfaces.ToolExecMeta{
			IdempotencyKey: input.IdempotencyKey(),
			RunID:          input.RunID,
			ToolCallID:     input.ToolCallID,
			Iteration:      input.Iteration,
		})
		result, execErr = tool.Execute(execCtx, args)
		if execErr == nil {
			content = fmt.Sprintf("%v", result)
		}
		if runHooks {
			var err error
			content, execErr, err = rt.runAfterToolHooks(ctx, input, hookedCall, content, execErr)
			if err != nil {
				sp.RecordError(err)
				rt.Metrics.IncrementCounter(ctx, types.MetricToolCallFailed, toolAttr)
				rt.Metrics.RecordHistogram(ctx, types.MetricToolLatencyMs, float64(time.Since(toolStart).Milliseconds()), toolAttr)
				return "", err
			}
		}
	}

	toolLatency := float64(time.Since(toolStart).Milliseconds())

	if execErr != nil {
		sp.RecordError(execErr)
		rt.Metrics.IncrementCounter(ctx, types.MetricToolCallFailed, toolAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricToolLatencyMs, toolLatency, toolAttr)
		return "", execErr
	}

	rt.Metrics.RecordHistogram(ctx, types.MetricToolLatencyMs, toolLatency, toolAttr)
	rt.Metrics.IncrementCounter(ctx, types.MetricToolCallCompleted, toolAttr)
	log.Debug(ctx, "runtime: tool execute completed", slog.String("scope", "runtime"), slog.String("tool", toolName))
	return content, nil
}

// executeRetrieverTool runs retrieve hooks, tool.Execute, and formats documents for the LLM.
// Metrics and tracing are handled by [Runtime.executeTool].
func (rt *Runtime) executeRetrieverTool(ctx context.Context, input ExecuteToolInput, tool interfaces.Tool, args map[string]any) (string, error) {
	retrieverName, ok := types.RetrieverNameFromToolName(tool.Name())
	if !ok {
		return "", fmt.Errorf("retriever tool: invalid tool name %q", tool.Name())
	}

	query, err := types.RetrieverToolParamQueryValue(args)
	if err != nil {
		return "", err
	}

	retrieveInput := ExecuteRetrieversInput{
		RunID:     input.RunID,
		Iteration: input.Iteration,
		Query:     query,
	}

	call, err := rt.runBeforeRetrieveHooks(ctx, retrieveInput, types.RetrieverModeAgentic, retrieverName)
	if err != nil {
		return "", err
	}

	execArgs := map[string]any{types.RetrieverToolParamQuery: call.Query}
	result, execErr := tool.Execute(ctx, execArgs)
	if execErr != nil {
		return "", execErr
	}

	docs, ok := result.([]interfaces.Document)
	if !ok {
		return "", fmt.Errorf("retriever tool: unexpected result type %T", result)
	}

	docs, err = rt.runAfterRetrieveHooks(ctx, retrieveInput, call, docs)
	if err != nil {
		return "", err
	}

	content := FormatRetrieverDocs(docs)
	if content == "" {
		input.Logger.Warn(ctx, "runtime: retriever returned no documents",
			slog.String("scope", "runtime"),
			slog.String("tool", tool.Name()),
			slog.String("retriever", retrieverName),
			slog.String("query", call.Query),
		)
	}
	return content, nil
}

func (rt *Runtime) executeSaveMemoryTool(ctx context.Context, input ExecuteToolInput, scope interfaces.MemoryScope) (string, error) {
	log := input.Logger
	args := input.Args
	toolAttr := interfaces.Attribute{Key: types.MetricAttrTool, Value: types.SaveMemoryToolName}
	rt.Metrics.IncrementCounter(ctx, types.MetricToolCallStarted, toolAttr)
	toolStart := time.Now()

	ctx, sp := rt.Tracer.StartSpan(ctx, "tool.execute",
		interfaces.Attribute{Key: "tool.name", Value: types.SaveMemoryToolName},
		interfaces.Attribute{Key: "arg.count", Value: len(args)},
	)
	defer sp.End()

	record, err := parseSaveMemoryToolArgs(args)
	if err != nil {
		toolLatency := float64(time.Since(toolStart).Milliseconds())
		sp.RecordError(err)
		rt.Metrics.IncrementCounter(ctx, types.MetricToolCallFailed, toolAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricToolLatencyMs, toolLatency, toolAttr)
		return "", err
	}

	if err := rt.StoreMemoryRecords(ctx, StoreMemoryRecordsInput{
		Logger:    log,
		RunID:     input.RunID,
		Iteration: input.Iteration,
		Scope:     scope,
		Records:   []interfaces.MemoryRecord{record},
	}); err != nil {
		toolLatency := float64(time.Since(toolStart).Milliseconds())
		sp.RecordError(err)
		rt.Metrics.IncrementCounter(ctx, types.MetricToolCallFailed, toolAttr)
		rt.Metrics.RecordHistogram(ctx, types.MetricToolLatencyMs, toolLatency, toolAttr)
		return "", err
	}

	toolLatency := float64(time.Since(toolStart).Milliseconds())
	rt.Metrics.RecordHistogram(ctx, types.MetricToolLatencyMs, toolLatency, toolAttr)
	rt.Metrics.IncrementCounter(ctx, types.MetricToolCallCompleted, toolAttr)
	return "memory saved", nil
}

// AuthorizeTool checks programmatic authorization for a tool before approval/execution.
// Tools that do not implement interfaces.ToolAuthorizer are allowed by default.
func (rt *Runtime) AuthorizeTool(ctx context.Context, log logger.Logger, tools []interfaces.Tool, toolName string, args map[string]any) (AuthorizeResult, error) {
	log.Debug(ctx, "runtime: tool authorize started", slog.String("scope", "runtime"), slog.String("tool", toolName), slog.Int("argCount", len(args)))

	tool, ok := FindToolByName(tools, toolName)
	if !ok {
		log.Warn(ctx, "runtime: unknown tool in authorization", slog.String("scope", "runtime"), slog.String("tool", toolName))
		return AuthorizeResult{}, fmt.Errorf("unknown tool: %s", toolName)
	}

	authorizer, ok := tool.(interfaces.ToolAuthorizer)
	if !ok {
		log.Debug(ctx, "runtime: tool has no authorizer; allow by default", slog.String("scope", "runtime"), slog.String("tool", toolName))
		return AuthorizeResult{Allowed: true}, nil
	}

	ctx, sp := rt.Tracer.StartSpan(ctx, "tool.authorize",
		interfaces.Attribute{Key: "tool.name", Value: toolName},
		interfaces.Attribute{Key: "arg.count", Value: len(args)},
	)
	defer sp.End()

	decision, err := authorizer.Authorize(ctx, args)
	if err != nil {
		sp.RecordError(err)
		log.Warn(ctx, "runtime: tool authorization failed", slog.String("scope", "runtime"), slog.String("tool", toolName), slog.Any("error", err))
		return AuthorizeResult{}, err
	}

	if decision.Allow {
		sp.SetAttribute("decision", "allowed")
		log.Debug(ctx, "runtime: tool authorization allowed", slog.String("scope", "runtime"), slog.String("tool", toolName))
		return AuthorizeResult{Allowed: true}, nil
	}

	reason := strings.TrimSpace(decision.Reason)
	sp.SetAttribute("decision", "denied")
	sp.SetAttribute("deny.reason", reason)
	log.Info(ctx, "runtime: tool authorization denied", slog.String("scope", "runtime"), slog.String("tool", toolName), slog.String("reason", reason))
	return AuthorizeResult{Allowed: false, Reason: reason}, nil
}

// ExecuteRetrievers runs all configured retrievers in parallel for the given query and
// returns a combined document context string for injection into the LLM system prompt.
// Partial failures are logged and skipped; all retrievers failing returns an error.
func (rt *Runtime) ExecuteRetrievers(ctx context.Context, input ExecuteRetrieversInput) (*RetrieverResult, error) {
	log := input.Logger
	query := input.Query

	retrievers := rt.AgentConfig.Retrievers.Retrievers
	if len(retrievers) == 0 {
		return &RetrieverResult{}, nil
	}

	mode := rt.AgentConfig.Retrievers.Mode

	log.Debug(ctx, "runtime: retriever prefetch started", slog.String("scope", "runtime"), slog.Int("retrieverCount", len(retrievers)), slog.String("query", query))

	type retrieverResult struct {
		name string
		docs []interfaces.Document
		err  error
	}

	results := make([]retrieverResult, len(retrievers))
	var wg sync.WaitGroup
	for i, r := range retrievers {
		wg.Add(1)
		go func(idx int, ret interfaces.Retriever) {
			defer wg.Done()
			name := ret.Name()
			retrieverAttr := interfaces.Attribute{Key: types.MetricAttrRetriever, Value: name}
			rt.Metrics.IncrementCounter(ctx, types.MetricRetrieverCallStarted, retrieverAttr)

			call, hookErr := rt.runBeforeRetrieveHooks(ctx, input, mode, name)
			if hookErr != nil {
				rt.Metrics.IncrementCounter(ctx, types.MetricRetrieverCallFailed, retrieverAttr)
				results[idx] = retrieverResult{name: name, err: hookErr}
				return
			}

			searchCtx, sp := rt.Tracer.StartSpan(ctx, "retriever.search",
				interfaces.Attribute{Key: "retriever.name", Value: name},
				interfaces.Attribute{Key: "query", Value: call.Query},
			)

			start := time.Now()
			docs, err := ret.Search(searchCtx, call.Query)
			latency := float64(time.Since(start).Milliseconds())
			if err != nil {
				sp.RecordError(err)
				sp.End()
				rt.Metrics.IncrementCounter(ctx, types.MetricRetrieverCallFailed, retrieverAttr)
				rt.Metrics.RecordHistogram(ctx, types.MetricRetrieverLatencyMs, latency, retrieverAttr)
				results[idx] = retrieverResult{name: name, docs: docs, err: err}
				return
			}

			docs, hookErr = rt.runAfterRetrieveHooks(searchCtx, input, call, docs)
			if hookErr != nil {
				sp.RecordError(hookErr)
				sp.End()
				rt.Metrics.IncrementCounter(ctx, types.MetricRetrieverCallFailed, retrieverAttr)
				rt.Metrics.RecordHistogram(ctx, types.MetricRetrieverLatencyMs, latency, retrieverAttr)
				results[idx] = retrieverResult{name: name, err: hookErr}
				return
			}

			sp.End()
			rt.Metrics.IncrementCounter(ctx, types.MetricRetrieverCallCompleted, retrieverAttr)
			rt.Metrics.RecordHistogram(ctx, types.MetricRetrieverLatencyMs, latency, retrieverAttr)
			results[idx] = retrieverResult{name: name, docs: docs, err: nil}
		}(i, r)
	}
	wg.Wait()

	multipleRetrievers := len(retrievers) > 1
	var sb strings.Builder
	failedCount := 0
	for _, res := range results {
		if res.err != nil {
			failedCount++
			log.Error(ctx, "runtime: retriever search failed, skipping", slog.String("scope", "runtime"), slog.String("retriever", res.name), slog.Any("error", res.err))
			continue
		}
		if len(res.docs) == 0 {
			continue
		}
		if multipleRetrievers {
			fmt.Fprintf(&sb, "## %s\n", res.name)
		}
		sb.WriteString(FormatRetrieverDocs(res.docs))
	}

	if failedCount > 0 {
		log.Warn(ctx, "runtime: some retrievers failed, continuing with partial context", slog.String("scope", "runtime"), slog.Int("failed", failedCount), slog.Int("total", len(retrievers)))
	}

	retrieverContext := strings.TrimSpace(sb.String())
	log.Debug(ctx, "runtime: retriever prefetch completed", slog.String("scope", "runtime"), slog.Int("retrieverCount", len(retrievers)), slog.Bool("hasContext", retrieverContext != ""))
	return &RetrieverResult{
		Context:        retrieverContext,
		TotalSearches:  int64(len(retrievers)),
		FailedSearches: int64(failedCount),
	}, nil
}

// MemoryConfigured reports whether long-term memory is wired on the runtime.
func (rt *Runtime) MemoryConfigured() bool {
	return rt.AgentConfig.Memory.Config != nil && rt.AgentConfig.Memory.Config.Memory != nil
}

// RecallEnabled reports whether the SDK should load memories before each run.
func (rt *Runtime) RecallEnabled() bool {
	if !rt.MemoryConfigured() {
		return false
	}
	return rt.AgentConfig.Memory.Config.Recall.Enabled
}

// RunEndMemoryStoreEnabled reports whether run-end memory store runs ([memory.StoreModeAlways]).
func (rt *Runtime) RunEndMemoryStoreEnabled() bool {
	if !rt.MemoryConfigured() {
		return false
	}
	return rt.AgentConfig.Memory.Config.Store.Mode == memory.StoreModeAlways
}

// MemoryStoreOnDemand reports whether save_memory tool store is active.
func (rt *Runtime) MemoryStoreOnDemand() bool {
	if !rt.MemoryConfigured() {
		return false
	}
	return rt.AgentConfig.Memory.Config.Store.Mode == memory.StoreModeOnDemand
}

// ResolveMemoryScope builds scope from the request context using configured resolvers.
func (rt *Runtime) ResolveMemoryScope(ctx context.Context) (interfaces.MemoryScope, error) {
	if !rt.MemoryConfigured() {
		return interfaces.MemoryScope{}, nil
	}
	return rt.AgentConfig.Memory.Config.ScopeConfig.Resolve(ctx)
}

// FormatMemoryEntries formats memories for injection into the LLM system prompt.
func FormatMemoryEntries(entries []interfaces.MemoryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, entry := range entries {
		fmt.Fprintf(&sb, types.MemoryEntryFormat, i+1, entry.Text, entry.Kind, entry.Score)
	}
	return sb.String()
}

// ExecuteMemoryRecall loads scoped memories for query and returns formatted prompt context.
func (rt *Runtime) ExecuteMemoryRecall(ctx context.Context, input ExecuteMemoryRecallInput) (*MemoryResult, error) {
	cfg := rt.AgentConfig.Memory.Config
	if cfg == nil || cfg.Memory == nil {
		return &MemoryResult{}, nil
	}

	log := input.Logger
	query := input.Query
	scope := input.Scope

	log.Debug(ctx, "runtime: memory recall started",
		slog.String("scope", "runtime"),
		slog.String("query", query))

	rt.Metrics.IncrementCounter(ctx, types.MetricMemoryRecallStarted)
	start := time.Now()

	ctx, sp := rt.Tracer.StartSpan(ctx, "memory.recall",
		interfaces.Attribute{Key: "query", Value: query},
	)
	defer sp.End()

	recall := cfg.Recall
	loadCall := hooks.MemoryLoadCall{
		Scope:    scope,
		Query:    query,
		Limit:    recall.Limit,
		MinScore: recall.MinScore,
		Kinds:    recall.Kinds,
	}
	loadCall, err := rt.runBeforeMemoryLoadHooks(ctx, input, loadCall)
	if err != nil {
		latency := float64(time.Since(start).Milliseconds())
		sp.RecordError(err)
		sp.SetAttribute("latency_ms", latency)
		rt.Metrics.IncrementCounter(ctx, types.MetricMemoryRecallFailed)
		rt.Metrics.RecordHistogram(ctx, types.MetricMemoryRecallLatencyMs, latency)
		return nil, err
	}

	entries, err := cfg.Memory.Load(ctx, loadCall.Scope, loadCall.Query, loadOptionsFromCall(loadCall, true)...)
	if err != nil {
		latency := float64(time.Since(start).Milliseconds())
		sp.RecordError(err)
		sp.SetAttribute("latency_ms", latency)
		rt.Metrics.IncrementCounter(ctx, types.MetricMemoryRecallFailed)
		rt.Metrics.RecordHistogram(ctx, types.MetricMemoryRecallLatencyMs, latency)
		log.Error(ctx, "runtime: memory recall failed", slog.String("scope", "runtime"), slog.Any("error", err))
		return nil, fmt.Errorf("memory recall: %w", err)
	}

	// Semantic recall often misses distilled memories; fall back to scoped recency list.
	if len(entries) == 0 && strings.TrimSpace(loadCall.Query) != "" {
		log.Debug(ctx, "runtime: memory recall semantic empty, trying recency fallback",
			slog.String("scope", "runtime"))
		fallback, fbErr := cfg.Memory.Load(ctx, loadCall.Scope, "", cfg.Recall.RecencyLoadOptions()...)
		if fbErr != nil {
			latency := float64(time.Since(start).Milliseconds())
			sp.RecordError(fbErr)
			sp.SetAttribute("latency_ms", latency)
			rt.Metrics.IncrementCounter(ctx, types.MetricMemoryRecallFailed)
			rt.Metrics.RecordHistogram(ctx, types.MetricMemoryRecallLatencyMs, latency)
			log.Error(ctx, "runtime: memory recall fallback failed", slog.String("scope", "runtime"), slog.Any("error", fbErr))
			return nil, fmt.Errorf("memory recall: %w", fbErr)
		}
		entries = fallback
	}

	memoryContext := strings.TrimSpace(FormatMemoryEntries(entries))
	memoryContext, err = rt.runAfterMemoryLoadHooks(ctx, input, loadCall, memoryContext)
	if err != nil {
		latency := float64(time.Since(start).Milliseconds())
		sp.RecordError(err)
		sp.SetAttribute("latency_ms", latency)
		rt.Metrics.IncrementCounter(ctx, types.MetricMemoryRecallFailed)
		rt.Metrics.RecordHistogram(ctx, types.MetricMemoryRecallLatencyMs, latency)
		return nil, err
	}

	latency := float64(time.Since(start).Milliseconds())
	sp.SetAttribute("entry.count", len(entries))
	sp.SetAttribute("has_context", memoryContext != "")
	sp.SetAttribute("latency_ms", latency)
	rt.Metrics.IncrementCounter(ctx, types.MetricMemoryRecallCompleted)
	rt.Metrics.RecordHistogram(ctx, types.MetricMemoryRecallLatencyMs, latency)
	log.Debug(ctx, "runtime: memory recall completed",
		slog.String("scope", "runtime"),
		slog.Int("entries", len(entries)),
		slog.Bool("hasContext", memoryContext != ""))

	return &MemoryResult{
		Context:       memoryContext,
		TotalRecalls:  1,
		FailedRecalls: 0,
	}, nil
}

// ExecuteMemoryStore extracts long-term memories from the run and persists them in scope.
func (rt *Runtime) ExecuteMemoryStore(ctx context.Context, input ExecuteMemoryStoreInput) error {
	if !rt.RunEndMemoryStoreEnabled() {
		return nil
	}

	log := input.Logger
	extract := rt.resolveMemoryExtractFunc()
	if extract == nil {
		rt.recordMemoryExtractUnavailable(ctx, log)
		return nil
	}

	records, err := rt.extractMemoryRecords(ctx, log, input.Messages, extract)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	return rt.StoreMemoryRecords(ctx, StoreMemoryRecordsInput{
		Logger:    log,
		RunID:     input.RunID,
		Iteration: input.Iteration,
		Scope:     input.Scope,
		Records:   records,
	})
}
