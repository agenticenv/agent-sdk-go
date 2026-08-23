package restate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/google/uuid"
	restatesdk "github.com/restatedev/sdk-go"
	restateingress "github.com/restatedev/sdk-go/ingress"
)

// AgentLoop is the Restate service that runs the durable agent loop.
// Bound via restatesdk.Reflect(AgentLoop{rt: r}) in NewRestateRuntime.
// Value receivers are required for Reflect to discover handlers.
type AgentLoop struct {
	rt *RestateRuntime
}

// ServiceName returns the Restate service name (AgentLoop_<agentName>).
func (s AgentLoop) ServiceName() string {
	if s.rt == nil || s.rt.agentLoopServiceName == "" {
		return agentLoopServiceName
	}
	return s.rt.agentLoopServiceName
}

// agentLoopCore is the shared per-run fields for the Restate wire payload and the
// in-process loop input. Embedded anonymously so JSON stays flat (same wire shape).
type agentLoopCore struct {
	RunID          string `json:"run_id"`
	UserPrompt     string `json:"user_prompt"`
	ConversationID string `json:"conversation_id,omitempty"`
	// EventTopic is the AgentEventLog PubSub key. Root runs use run_id; child runs
	// inherit the root topic so stream consumers see sub-agent events.
	EventTopic string `json:"event_topic,omitempty"`
	// EventLogService is the AgentEventLog virtual-object service that owns the stream
	// (e.g. AgentEventLog_main). Empty means this runtime's own event log. Analogous to
	// Temporal RootWorkflowID: when a parent delegates, the child publishes here.
	EventLogService  string                   `json:"event_log_service,omitempty"`
	EventTypes       []events.AgentEventType  `json:"event_types,omitempty"`
	MaxSubAgentDepth int                      `json:"max_sub_agent_depth,omitempty"`
	SubAgentDepth    int                      `json:"sub_agent_depth,omitempty"`
	SubAgentRoutes   map[string]SubAgentRoute `json:"sub_agent_routes,omitempty"`
	MemoryScope      interfaces.MemoryScope   `json:"memory_scope,omitempty"`
}

// AgentLoopRequest is the JSON ingress payload for AgentLoop.Run and .Stream.
// Non-serializable per-run state (tools, eventTypes) is stashed in rt.tools.stash.
type AgentLoopRequest struct {
	agentLoopCore
	AgentName        string `json:"agent_name,omitempty"`
	LLMStreamEnabled bool   `json:"llm_stream_enabled"`
	StreamHandler    bool   `json:"stream_handler"`
}

// AgentLoopResponse is the durable result serialised by Restate when the handler completes.
type AgentLoopResponse struct {
	Result *types.AgentRunResult `json:"result,omitempty"`
}

// CancelRequest requests cancellation of an in-flight agent loop invocation.
type CancelRequest struct {
	RunID        string `json:"run_id,omitempty"`
	InvocationID string `json:"invocation_id"`
}

// AgentLoopInput holds per-run execution inputs for one durable Restate agent run.
// Shares [agentLoopCore] with [AgentLoopRequest]; adds resolved tools and stream flags.
// Mirrors AgentLoopInput (local) / AgentWorkflowInput (Temporal) field semantics.
type AgentLoopInput struct {
	agentLoopCore
	// StreamingEnabled requests token-level LLM streaming.
	// Only active when IsStreamHandler is also true (the Stream handler enables both).
	StreamingEnabled bool
	// IsStreamHandler is true when this input is for the Stream handler path.
	// Stream runs publish events to AgentEventLog; Run runs publish only approval events.
	IsStreamHandler bool
	// Tools is the resolved tool list for this run (not on the Restate wire payload).
	Tools []interfaces.Tool
}

// AgentLoopResult is the outcome of a completed durable agent run.
// Mirrors AgentLoopResult (local) and AgentWorkflowResult (Temporal) — same fields, same semantics.
type AgentLoopResult struct {
	Content   string
	LLMUsage  *interfaces.LLMUsage
	Telemetry *types.AgentTelemetry
}

// toolResult holds the output of a single tool execution step.
// Mirrors toolResult in the local runtime.
type toolResult struct {
	message  interfaces.Message
	failed   bool // true: hard error, ExecuteTool failure, or context cancellation
	llmUsage *interfaces.LLMUsage
}

// toolCallState tracks per-tool approval and completion status during parallel execution.
type toolCallState struct {
	tc       base.ToolCallRequest
	approval types.ApprovalStatus
	done     bool
}

// pendingApproval holds the durable futures for one tool awaiting human approval.
type pendingApproval struct {
	index   int
	awake   restatesdk.AwakeableFuture[types.ApprovalStatus]
	timeout restatesdk.AfterFuture
}

// parallelExecFuture pairs a tool index with its RunAsync execution future.
type parallelExecFuture struct {
	index int
	fut   restatesdk.RunAsyncFuture[string]
}

// User-facing content when a tool cannot run due to approval outcome.
const (
	msgToolRejected            = "Tool execution was rejected by the user."
	msgToolApprovalUnavailable = "Tool approval could not be completed because no approval handler is configured; continuing without running the tool."
	msgToolUnauthorized        = "Tool execution was denied by authorization policy."
	msgToolApprovalTimedOut    = "Tool approval timed out; continuing without running the tool."
)

// Run is the durable non-streaming agent loop entry point.
func (s AgentLoop) Run(ctx restatesdk.Context, req AgentLoopRequest) (*AgentLoopResponse, error) {
	req.StreamHandler = false
	return s.handle(ctx, req)
}

// Stream is the durable streaming agent loop entry point.
func (s AgentLoop) Stream(ctx restatesdk.Context, req AgentLoopRequest) (*AgentLoopResponse, error) {
	req.StreamHandler = true
	return s.handle(ctx, req)
}

// Cancel cancels an in-flight agent loop invocation via Restate's native CancelInvocation.
// Stateless: does not need the runtime pointer.
func (AgentLoop) Cancel(ctx restatesdk.Context, req CancelRequest) error {
	invocationID := strings.TrimSpace(req.InvocationID)
	if invocationID == "" {
		return fmt.Errorf("restate: Cancel requires invocation_id")
	}
	restatesdk.CancelInvocation(ctx, invocationID)
	return nil
}

// handle resolves per-run inputs, runs the durable loop, then cleans up stash / event log.
func (s AgentLoop) handle(ctx restatesdk.Context, req AgentLoopRequest) (*AgentLoopResponse, error) {
	if s.rt == nil {
		return nil, fmt.Errorf("restate: AgentLoop has no runtime")
	}

	if err := s.rt.validateAgentName(req.AgentName); err != nil {
		return nil, err
	}

	stdCtx := context.Context(ctx)
	staged := s.rt.loadStagedRun(req.RunID)

	tools, err := s.rt.resolveTools(stdCtx, req.RunID)
	if err != nil {
		return nil, err
	}
	if len(tools) == 0 {
		tools = staged.tools
	}

	eventTypes := staged.eventTypes
	if len(eventTypes) == 0 {
		eventTypes = req.EventTypes
	}
	if len(eventTypes) == 0 {
		if req.StreamHandler {
			eventTypes = []events.AgentEventType{events.AgentEventAll}
		} else if s.rt.approvalHandler != nil {
			eventTypes = []events.AgentEventType{events.AgentEventTypeCustom}
		}
	}

	maxDepth := staged.maxSubAgentDepth
	if maxDepth == 0 {
		maxDepth = req.MaxSubAgentDepth
	}

	memoryScope := req.MemoryScope
	if !isMemoryScopeSet(memoryScope) {
		memoryScope, err = s.rt.ResolveMemoryScope(stdCtx)
		if err != nil {
			s.rt.logger.Warn(stdCtx, "restate: memory scope resolve failed, using empty scope",
				slog.String("scope", "runtime"),
				slog.Any("error", err))
			memoryScope = interfaces.MemoryScope{}
		}
	}

	eventTopic := strings.TrimSpace(req.EventTopic)
	if eventTopic == "" {
		eventTopic = req.RunID
	}

	core := req.agentLoopCore
	core.EventTypes = eventTypes
	core.MemoryScope = memoryScope
	core.EventTopic = eventTopic
	core.EventLogService = strings.TrimSpace(req.EventLogService)
	core.MaxSubAgentDepth = maxDepth

	result, err := s.rt.executeAgentLoop(ctx, AgentLoopInput{
		agentLoopCore:    core,
		StreamingEnabled: req.LLMStreamEnabled && req.StreamHandler,
		IsStreamHandler:  req.StreamHandler,
		Tools:            tools,
	})
	if err != nil {
		if isApplicationLoopError(err) {
			s.rt.tools.stash.Delete(req.RunID)
		}
		return nil, terminalLoopError(err)
	}

	// Release the stash entry after success.
	// Kept until here so Restate retries/replays can reload the stash on the same pod.
	s.rt.tools.stash.Delete(req.RunID)

	// Delayed Clear wipes AgentEventLog after readers have time to drain (EventLog.TTL).
	// Only the root run owns the topic; child runs publish to the parent's log and must not Clear it.
	// Stream always publishes; Run publishes only when EventTypes is non-empty (approvals).
	// Skipped when EventLog.DisableClear is set (useful for short-lived examples).
	if s.rt.eventLogCleanupEnabled() &&
		eventTopic != "" && req.SubAgentDepth == 0 && (req.StreamHandler || len(eventTypes) > 0) {
		delay := s.rt.eventLogTTL()
		restatesdk.ObjectSend(ctx, s.rt.eventLogServiceName, eventTopic, "Clear").
			Send(restatesdk.Void{}, restatesdk.WithDelay(delay))
	}

	model := ""
	if s.rt.AgentConfig.LLM.Client != nil {
		model = s.rt.AgentConfig.LLM.Client.GetModel()
	}
	return &AgentLoopResponse{
		Result: &types.AgentRunResult{
			Content:   result.Content,
			AgentName: strings.TrimSpace(s.rt.AgentSpec.Name),
			Model:     model,
			Metadata:  map[string]any{},
			RunID:     req.RunID,
			LLMUsage:  result.LLMUsage,
			Telemetry: result.Telemetry,
		},
	}, nil
}

// validateAgentName accepts empty or this runtime's agent name. Each AgentLoop service
// executes only its own agent; sub-agents are separate Restate services.
func (rt *RestateRuntime) validateAgentName(agentName string) error {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" || strings.EqualFold(agentName, strings.TrimSpace(rt.AgentSpec.Name)) {
		return nil
	}
	return fmt.Errorf("restate: agent name %q does not match this AgentLoop (%q)", agentName, rt.AgentSpec.Name)
}

// isApplicationLoopError reports errors that finish the run (do not retry).
func isApplicationLoopError(err error) bool {
	return errors.Is(err, types.ErrBudgetExceeded) || errors.Is(err, types.ErrBudgetApprovalUnavailable)
}

// terminalLoopError marks application-complete failures as Restate terminal so
// the invocation is not retried (BudgetStopRun would otherwise loop forever).
// ToTerminalError copies the message only, so the sentinel is re-wrapped for errors.Is.
func terminalLoopError(err error) error {
	if err == nil || restatesdk.IsTerminalError(err) {
		return err
	}
	switch {
	case errors.Is(err, types.ErrBudgetExceeded):
		return fmt.Errorf("%w: %w", types.ErrBudgetExceeded, restatesdk.ToTerminalError(err))
	case errors.Is(err, types.ErrBudgetApprovalUnavailable):
		return fmt.Errorf("%w: %w", types.ErrBudgetApprovalUnavailable, restatesdk.ToTerminalError(err))
	}
	return err
}

// isMemoryScopeSet reports whether any field of a MemoryScope is populated.
func isMemoryScopeSet(scope interfaces.MemoryScope) bool {
	return strings.TrimSpace(scope.UserID) != "" ||
		strings.TrimSpace(scope.TenantID) != "" ||
		strings.TrimSpace(scope.AgentID) != "" ||
		len(scope.Tags) > 0
}

// ─── Agent loop ──────────────────────────────────────────────────────────────

// executeAgentLoop executes the agent loop with Restate durable steps, awakeable
// approvals, and AgentEventLog publishes.
func (rt *RestateRuntime) executeAgentLoop(ctx restatesdk.Context, input AgentLoopInput) (*AgentLoopResult, error) {
	log := rt.logger
	telemetry := base.NewAgentTelemetry(time.Now())
	agentName := rt.AgentSpec.Name
	model := ""
	if rt.AgentConfig.LLM.Client != nil {
		model = rt.AgentConfig.LLM.Client.GetModel()
	}

	otelCtx, span := rt.Tracer.StartSpan(ctx, "agent.loop",
		interfaces.Attribute{Key: "agent.name", Value: agentName},
		interfaces.Attribute{Key: "model", Value: model},
	)
	defer span.End()
	ctx = restatesdk.WrapContext(ctx, otelCtx)

	policies := rt.executionPolicies()
	maxIter := rt.AgentConfig.Limits.MaxIterations
	if maxIter <= 0 {
		maxIter = 10
	}

	topic := strings.TrimSpace(input.EventTopic)
	if topic == "" {
		topic = input.RunID
	}
	eventLogService := strings.TrimSpace(input.EventLogService)
	if eventLogService == "" {
		eventLogService = rt.eventLogServiceName
	}
	// Internal emit: publish events to the stream-owner AgentEventLog for this run.
	// EventTypes acts as a publish filter: empty = no events; [AgentEventAll] = all; otherwise only listed types.
	emit := func(ev events.AgentEvent) { rt.emitEvent(ctx, eventLogService, topic, input.EventTypes, ev) }

	// Build initial message list from user prompt.
	messages := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: input.UserPrompt}}
	persistedCount := 0

	// Prepend conversation history when conversation memory is configured for this run.
	if rt.conversationMemoryEnabled(input) {
		convMsgs, err := executeWithPolicy(ctx, "conversation-load", policies.Conversation, func(runCtx restatesdk.RunContext) ([]interfaces.Message, error) {
			return rt.FetchConversationMessages(runCtx, log, input.ConversationID)
		})
		if err != nil {
			log.Warn(ctx, "restate: failed to load conversation history, continuing without it",
				slog.String("scope", "loop"),
				slog.String("conversationID", input.ConversationID),
				slog.Any("error", err))
		} else {
			messages = append(convMsgs, messages...)
			persistedCount = len(convMsgs)
		}
	}

	// Pre-fetch long-term memory context when recall is enabled.
	memoryContext := ""
	if rt.MemoryConfigured() && rt.RecallEnabled() {
		res, err := executeWithPolicy(ctx, "memory-recall", policies.Memory, func(runCtx restatesdk.RunContext) (*base.MemoryResult, error) {
			return rt.ExecuteMemoryRecall(runCtx, base.ExecuteMemoryRecallInput{
				Logger:    log,
				RunID:     input.RunID,
				Iteration: 0,
				Scope:     input.MemoryScope,
				Query:     input.UserPrompt,
			})
		})
		if err != nil {
			return nil, fmt.Errorf("memory recall: %w", err)
		}
		memoryContext = res.Context
		telemetry.Storage.TotalMemoryRecalls += res.TotalRecalls
		telemetry.Storage.FailedMemoryRecalls += res.FailedRecalls
	}

	// Pre-fetch retriever context for prefetch/hybrid modes.
	retrieverContext := ""
	retrieverMode := rt.AgentConfig.Retrievers.Mode
	if (retrieverMode == types.RetrieverModePrefetch || retrieverMode == types.RetrieverModeHybrid) &&
		len(rt.AgentConfig.Retrievers.Retrievers) > 0 {
		res, err := executeWithPolicy(ctx, "retriever-prefetch", policies.Retriever, func(runCtx restatesdk.RunContext) (*base.RetrieverResult, error) {
			return rt.ExecuteRetrievers(runCtx, base.ExecuteRetrieversInput{
				Logger:    log,
				RunID:     input.RunID,
				Iteration: 0,
				Query:     input.UserPrompt,
			})
		})
		if err != nil {
			return nil, fmt.Errorf("retriever prefetch: %w", err)
		}
		retrieverContext = res.Context
		telemetry.Storage.TotalRetrieverSearches += res.TotalSearches
		telemetry.Storage.FailedRetrieverSearches += res.FailedSearches
		telemetry.Storage.PrefetchSearches += res.TotalSearches
	}

	// Budget tracker: nil when no budget is configured.
	// enforceBudget is true on root runs (EventTopic not inherited from a parent).
	budgetTracker := base.NewBudgetTracker(rt.AgentConfig.Limits.Budget)
	enforceBudget := budgetTracker != nil && input.SubAgentDepth == 0

	var lastContent string
	var llmUsage *interfaces.LLMUsage

	for iter := 0; iter < maxIter; iter++ {
		messageID, err := executeWithPolicy(ctx, fmt.Sprintf("message-id-%d", iter),
			sdkruntime.ExecutionPolicy{MaxAttempts: 1},
			func(restatesdk.RunContext) (string, error) { return uuid.New().String(), nil })
		if err != nil {
			return nil, err
		}

		llmResult, err := rt.executeLLMStep(ctx, input, policies.LLM, base.ExecuteLLMInput{
			Logger:           log,
			AgentName:        agentName,
			MessageID:        messageID,
			RunID:            input.RunID,
			Iteration:        iter,
			Messages:         messages,
			SkipTools:        false,
			MemoryContext:    memoryContext,
			RetrieverContext: retrieverContext,
			Tools:            input.Tools,
		}, iter)
		if err != nil {
			return nil, err
		}
		telemetry.Run.TotalLLMCalls++
		llmUsage = base.MergeLLMUsage(llmUsage, llmResult.Usage)
		if budgetErr := rt.checkBudget(ctx, budgetTracker, enforceBudget, telemetry, llmResult.Usage, emit); budgetErr != nil {
			return &AgentLoopResult{Content: lastContent, LLMUsage: llmUsage, Telemetry: telemetry}, budgetErr
		}

		if len(llmResult.ToolCalls) == 0 {
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			lastContent = llmResult.Content
			break
		}

		if iter == maxIter-1 {
			finalMsgID, idErr := executeWithPolicy(ctx, fmt.Sprintf("message-id-final-%d", iter),
				sdkruntime.ExecutionPolicy{MaxAttempts: 1},
				func(restatesdk.RunContext) (string, error) { return uuid.New().String(), nil })
			if idErr != nil {
				return nil, idErr
			}
			llmResult, err = rt.executeLLMStep(ctx, input, policies.LLM, base.ExecuteLLMInput{
				Logger:           log,
				AgentName:        agentName,
				MessageID:        finalMsgID,
				RunID:            input.RunID,
				Iteration:        iter,
				Messages:         messages,
				SkipTools:        true,
				MemoryContext:    memoryContext,
				RetrieverContext: retrieverContext,
				Tools:            input.Tools,
			}, iter)
			if err != nil {
				return nil, fmt.Errorf("llm final call (iter %d): %w", iter, err)
			}
			llmUsage = base.MergeLLMUsage(llmUsage, llmResult.Usage)
			if budgetErr := rt.checkBudget(ctx, budgetTracker, enforceBudget, telemetry, llmResult.Usage, emit); budgetErr != nil {
				return &AgentLoopResult{Content: lastContent, LLMUsage: llmUsage, Telemetry: telemetry}, budgetErr
			}
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			lastContent = llmResult.Content
			telemetry.Run.TotalLLMCalls++
			telemetry.Run.FinishReason = types.FinishReasonMaxIterations
			break
		}

		assistantMsg := interfaces.Message{
			Role:      interfaces.MessageRoleAssistant,
			Content:   llmResult.Content,
			ToolCalls: make([]*interfaces.ToolCall, len(llmResult.ToolCalls)),
		}
		for i, tc := range llmResult.ToolCalls {
			assistantMsg.ToolCalls[i] = &interfaces.ToolCall{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Args:       tc.Args,
			}
		}
		messages = append(messages, assistantMsg)

		toolExecMode := rt.ToolExecutionMode
		if toolExecMode == "" {
			toolExecMode = types.AgentToolExecutionModeParallel
		}
		var toolResults []toolResult
		switch toolExecMode {
		case types.AgentToolExecutionModeParallel:
			toolResults, err = rt.executeToolsParallel(ctx, input, messageID, iter, llmResult.ToolCalls, policies, emit)
		case types.AgentToolExecutionModeSequential:
			toolResults, err = rt.executeToolsSequential(ctx, input, messageID, iter, llmResult.ToolCalls, policies, emit)
		default:
			return nil, fmt.Errorf("invalid tool execution mode %q: use %q or %q",
				toolExecMode, types.AgentToolExecutionModeParallel, types.AgentToolExecutionModeSequential)
		}
		if err != nil {
			return nil, err
		}

		for idx, res := range toolResults {
			messages = append(messages, res.message)
			tc := llmResult.ToolCalls[idx]
			if tc.ToolKind.CountsTowardToolTelemetry() {
				telemetry.Tools.Record(tc.ToolName, res.failed)
			}
			if tc.ToolKind == types.ToolKindRetriever {
				telemetry.Storage.TotalRetrieverSearches++
				telemetry.Storage.AgenticSearches++
				if res.failed {
					telemetry.Storage.FailedRetrieverSearches++
				}
			}
			if tc.ToolName == types.SaveMemoryToolName {
				if res.failed {
					telemetry.Storage.FailedMemoryStores++
				} else {
					telemetry.Storage.TotalMemoryStores++
				}
			}
			if res.llmUsage != nil {
				llmUsage = base.MergeLLMUsage(llmUsage, res.llmUsage)
				if budgetErr := rt.checkBudget(ctx, budgetTracker, enforceBudget, telemetry, res.llmUsage, emit); budgetErr != nil {
					return &AgentLoopResult{Content: lastContent, LLMUsage: llmUsage, Telemetry: telemetry}, budgetErr
				}
			}
		}

		if rt.conversationMemoryEnabled(input) && rt.AgentConfig.Session.ConversationSaveOnIteration && len(messages) > persistedCount {
			rt.persistConversationMessages(ctx, input.ConversationID, messages[persistedCount:], policies.Conversation)
			persistedCount = len(messages)
		}
	}

	if rt.conversationMemoryEnabled(input) && len(messages) > persistedCount {
		rt.persistConversationMessages(ctx, input.ConversationID, messages[persistedCount:], policies.Conversation)
	}

	if rt.RunEndMemoryStoreEnabled() {
		if err := executeWithPolicyErr(ctx, "memory-store", policies.Memory, func(runCtx restatesdk.RunContext) error {
			return rt.ExecuteMemoryStore(runCtx, base.ExecuteMemoryStoreInput{
				Logger:    log,
				RunID:     input.RunID,
				Iteration: 0,
				Scope:     input.MemoryScope,
				Messages:  messages,
			})
		}); err != nil {
			log.Warn(ctx, "restate: memory store failed", slog.String("scope", "loop"), slog.Any("error", err))
			telemetry.Storage.FailedMemoryStores++
		} else {
			telemetry.Storage.TotalMemoryStores++
		}
	}

	telemetry.Run.CompletedAt = time.Now()
	log.Info(ctx, "restate: agent run completed",
		slog.String("scope", "loop"),
		slog.String("agentName", agentName),
		slog.String("model", model),
		slog.Int("contentLen", len(lastContent)))

	return &AgentLoopResult{Content: lastContent, LLMUsage: llmUsage, Telemetry: telemetry}, nil
}

// ─── LLM step ────────────────────────────────────────────────────────────────

// executeLLMStep runs one LLM call as a durable Restate step.
// When StreamingEnabled is true, token events are published via emitEventIngress inside the step.
// Token events are NOT re-emitted on Restate retry/replay — the journal stores only the final result.
func (rt *RestateRuntime) executeLLMStep(
	ctx restatesdk.Context,
	input AgentLoopInput,
	policy sdkruntime.ExecutionPolicy,
	llmIn base.ExecuteLLMInput,
	iter int,
) (*base.LLMResult, error) {
	return executeWithPolicy(ctx, fmt.Sprintf("llm-%d", iter), policy, func(runCtx restatesdk.RunContext) (*base.LLMResult, error) {
		in := llmIn
		if input.StreamingEnabled {
			topic := strings.TrimSpace(input.EventTopic)
			if topic == "" {
				topic = input.RunID
			}
			eventLogService := strings.TrimSpace(input.EventLogService)
			if eventLogService == "" {
				eventLogService = rt.eventLogServiceName
			}
			in.Emit = func(ev events.AgentEvent) {
				rt.emitEventIngress(runCtx, eventLogService, topic, input.EventTypes, ev)
			}
			return rt.ExecuteLLMStream(runCtx, in)
		}
		return rt.ExecuteLLM(runCtx, in)
	})
}

// ─── Tool execution ───────────────────────────────────────────────────────────

// executeToolsSequential executes tool calls one at a time, preserving order.
func (rt *RestateRuntime) executeToolsSequential(
	ctx restatesdk.Context,
	input AgentLoopInput,
	messageID string,
	iteration int,
	toolCalls []base.ToolCallRequest,
	policies sdkruntime.ExecutionPolicies,
	emit func(events.AgentEvent),
) ([]toolResult, error) {
	rt.logger.Info(ctx, "restate: tool execution (sequential)",
		slog.String("scope", "loop"), slog.Int("toolCount", len(toolCalls)))

	results := make([]toolResult, len(toolCalls))
	for idx, tc := range toolCalls {
		res, err := rt.executeSingleTool(ctx, input, messageID, iteration, tc, policies, emit)
		if err != nil {
			rt.logger.Info(ctx, "restate: sequential tool failed",
				slog.String("scope", "loop"),
				slog.Int("toolIndex", idx),
				slog.String("toolName", tc.ToolName),
				slog.Any("error", err))
			res = toolResult{
				message: interfaces.Message{
					Role:       interfaces.MessageRoleTool,
					Content:    "Tool execution failed: " + err.Error(),
					ToolName:   tc.ToolName,
					ToolCallID: tc.ToolCallID,
				},
				failed: true,
			}
		}
		results[idx] = res
	}
	return results, nil
}

// executeToolsParallel executes tool authorisation and native tool calls concurrently using
// RunAsync and awakeables (no goroutines — required for deterministic Restate replay).
// Sub-agent delegations run sequentially after all native tools finish because they
// require the handler's restatesdk.Context rather than a RunContext.
//
// Event pairing: TOOL_CALL_START/ARGS are emitted for all tools before any auth result is
// processed. TOOL_CALL_END/RESULT always follow START for every tool, including auth failures.
func (rt *RestateRuntime) executeToolsParallel(
	ctx restatesdk.Context,
	input AgentLoopInput,
	messageID string,
	iteration int,
	toolCalls []base.ToolCallRequest,
	policies sdkruntime.ExecutionPolicies,
	emit func(events.AgentEvent),
) ([]toolResult, error) {
	n := len(toolCalls)
	if n == 0 {
		return nil, nil
	}
	if n == 1 {
		return rt.executeToolsSequential(ctx, input, messageID, iteration, toolCalls, policies, emit)
	}

	rt.logger.Info(ctx, "restate: tool execution (parallel)",
		slog.String("scope", "loop"), slog.Int("toolCount", n))

	states := make([]toolCallState, n)
	for i, tc := range toolCalls {
		states[i] = toolCallState{tc: tc, approval: types.ApprovalStatusApproved}
	}
	results := make([]toolResult, n)

	writeResult := func(i int, content string, failed bool) {
		tc := states[i].tc
		emitToolComplete(emit, messageID, tc.ToolCallID, content)
		results[i] = toolResult{
			message: interfaces.Message{
				Role: interfaces.MessageRoleTool, Content: content,
				ToolName: tc.ToolName, ToolCallID: tc.ToolCallID,
			},
			failed: failed,
		}
		states[i].done = true
	}

	// Step 1: Emit TOOL_CALL_START / ARGS for all tools up front.
	for i := range states {
		tc := states[i].tc
		emit(events.NewAgentToolCallStartEvent(tc.ToolCallID, tc.ToolName, messageID))
		if argsJSON, err := json.Marshal(tc.Args); err == nil {
			if s := string(argsJSON); s != "" && s != "null" && s != "{}" {
				emit(events.NewAgentToolCallArgsEvent(tc.ToolCallID, s))
			}
		}
	}

	// Step 2: Authorise all tools in parallel via RunAsync, then collect results.
	authFuts := make([]restatesdk.RunAsyncFuture[base.AuthorizeResult], n)
	for i := range states {
		tc := states[i].tc
		authFuts[i] = restatesdk.RunAsync(ctx, func(runCtx restatesdk.RunContext) (base.AuthorizeResult, error) {
			return rt.AuthorizeTool(runCtx, rt.logger, input.Tools, tc.ToolName, tc.Args)
		}, policyToRunOpts("tool-auth-"+tc.ToolCallID, policies.ToolAuth)...)
	}
	for i := range states {
		authResult, err := authFuts[i].Result()
		if err != nil {
			writeResult(i, "Tool execution failed: "+err.Error(), true)
			continue
		}
		if !authResult.Allowed {
			content := msgToolUnauthorized
			if authResult.Reason != "" {
				content = fmt.Sprintf("%s Reason: %s", content, authResult.Reason)
			}
			writeResult(i, content, false)
		}
	}

	// Step 3: Create approval awakeables for all pending approvals (overlapped creation gives
	// effective parallelism), then wait for each in order.
	var approvals []pendingApproval
	for i := range states {
		if states[i].done || !states[i].tc.NeedsApproval {
			continue
		}
		if !input.IsStreamHandler && rt.approvalHandler == nil {
			states[i].approval = types.ApprovalStatusUnavailable
			continue
		}
		awake, timeout := emitApprovalRequest(ctx, rt, states[i].tc, input, emit)
		approvals = append(approvals, pendingApproval{index: i, awake: awake, timeout: timeout})
	}
	for _, p := range approvals {
		first, waitErr := restatesdk.WaitFirst(ctx, p.awake, p.timeout)
		if waitErr != nil {
			writeResult(p.index, "Tool execution failed: "+waitErr.Error(), true)
			continue
		}
		if first == p.timeout {
			if err := p.timeout.Done(); err != nil {
				writeResult(p.index, "Tool execution failed: "+err.Error(), true)
				continue
			}
			states[p.index].approval = types.ApprovalStatusTimedOut
			continue
		}
		status, err := p.awake.Result()
		if err != nil {
			writeResult(p.index, "Tool execution failed: "+err.Error(), true)
			continue
		}
		states[p.index].approval = status
	}

	// Step 4: Dispatch native tools in parallel via RunAsync; queue sub-agents for after.
	var execFuts []parallelExecFuture
	var subAgentIdxs []int
	for i := range states {
		if states[i].done {
			continue
		}
		tc := states[i].tc
		switch states[i].approval {
		case types.ApprovalStatusApproved:
			if _, isSubAgent := input.SubAgentRoutes[tc.ToolName]; isSubAgent {
				subAgentIdxs = append(subAgentIdxs, i)
				continue
			}
			toolPolicy := rt.toolExecutionPolicy(tc.ToolKind, policies)
			execFuts = append(execFuts, parallelExecFuture{
				index: i,
				fut: restatesdk.RunAsync(ctx, func(runCtx restatesdk.RunContext) (string, error) {
					return rt.ExecuteTool(runCtx, base.ExecuteToolInput{
						Logger:     rt.logger,
						Tools:      input.Tools,
						ToolName:   tc.ToolName,
						Args:       tc.Args,
						ToolCallID: tc.ToolCallID,
						RunID:      input.RunID,
						Iteration:  iteration,
					}, input.MemoryScope)
				}, policyToRunOpts("tool-exec-"+tc.ToolCallID, toolPolicy)...),
			})
		case types.ApprovalStatusRejected:
			writeResult(i, msgToolRejected, false)
		case types.ApprovalStatusUnavailable:
			writeResult(i, msgToolApprovalUnavailable, false)
		case types.ApprovalStatusTimedOut:
			writeResult(i, msgToolApprovalTimedOut, false)
		default:
			writeResult(i, fmt.Sprintf("unexpected approval status %q for tool %q", states[i].approval, tc.ToolName), true)
		}
	}
	for _, ef := range execFuts {
		content, execErr := ef.fut.Result()
		if execErr != nil {
			rt.logger.Info(ctx, "restate: parallel tool failed",
				slog.String("scope", "loop"),
				slog.Int("toolIndex", ef.index),
				slog.String("toolName", states[ef.index].tc.ToolName),
				slog.Any("error", execErr))
			writeResult(ef.index, "Tool execution failed: "+execErr.Error(), true)
		} else {
			writeResult(ef.index, content, false)
		}
	}
	for _, i := range subAgentIdxs {
		if states[i].done {
			continue
		}
		tc := states[i].tc
		content, usage, execErr := rt.delegateToSubAgent(ctx, input, tc, input.SubAgentRoutes[tc.ToolName], emit)
		if execErr != nil {
			writeResult(i, "Tool execution failed: "+execErr.Error(), true)
		} else {
			writeResult(i, content, false)
			results[i].llmUsage = usage
		}
	}
	return results, nil
}

// executeSingleTool authorises, awaits approval if needed, and executes a single tool call.
func (rt *RestateRuntime) executeSingleTool(
	ctx restatesdk.Context,
	input AgentLoopInput,
	messageID string,
	iteration int,
	tc base.ToolCallRequest,
	policies sdkruntime.ExecutionPolicies,
	emit func(events.AgentEvent),
) (toolResult, error) {
	emit(events.NewAgentToolCallStartEvent(tc.ToolCallID, tc.ToolName, messageID))
	if argsJSON, err := json.Marshal(tc.Args); err == nil {
		if s := string(argsJSON); s != "" && s != "null" && s != "{}" {
			emit(events.NewAgentToolCallArgsEvent(tc.ToolCallID, s))
		}
	}

	authResult, err := executeWithPolicy(ctx, "tool-auth-"+tc.ToolCallID, policies.ToolAuth, func(runCtx restatesdk.RunContext) (base.AuthorizeResult, error) {
		return rt.AuthorizeTool(runCtx, rt.logger, input.Tools, tc.ToolName, tc.Args)
	})
	if err != nil {
		return toolResult{}, fmt.Errorf("tool authorization error for %q: %w", tc.ToolName, err)
	}
	if !authResult.Allowed {
		content := msgToolUnauthorized
		if authResult.Reason != "" {
			content = fmt.Sprintf("%s Reason: %s", content, authResult.Reason)
		}
		emitToolComplete(emit, messageID, tc.ToolCallID, content)
		return toolResult{message: interfaces.Message{
			Role: interfaces.MessageRoleTool, Content: content,
			ToolName: tc.ToolName, ToolCallID: tc.ToolCallID,
		}}, nil
	}

	approvalStatus := types.ApprovalStatusApproved
	if tc.NeedsApproval {
		if !input.IsStreamHandler && rt.approvalHandler == nil {
			approvalStatus = types.ApprovalStatusUnavailable
		} else {
			approvalStatus, err = rt.awaitApproval(ctx, tc, input, emit)
			if err != nil {
				return toolResult{}, err
			}
		}
	}

	var content string
	failed := false
	var llmUsage *interfaces.LLMUsage
	subAgentRoute, isSubAgent := input.SubAgentRoutes[tc.ToolName]
	switch approvalStatus {
	case types.ApprovalStatusApproved:
		if isSubAgent {
			content, llmUsage, err = rt.delegateToSubAgent(ctx, input, tc, subAgentRoute, emit)
			if err != nil {
				return toolResult{}, err
			}
		} else {
			toolPolicy := rt.toolExecutionPolicy(tc.ToolKind, policies)
			result, execErr := executeWithPolicy(ctx, "tool-exec-"+tc.ToolCallID, toolPolicy, func(runCtx restatesdk.RunContext) (string, error) {
				return rt.ExecuteTool(runCtx, base.ExecuteToolInput{
					Logger:     rt.logger,
					Tools:      input.Tools,
					ToolName:   tc.ToolName,
					Args:       tc.Args,
					ToolCallID: tc.ToolCallID,
					RunID:      input.RunID,
					Iteration:  iteration,
				}, input.MemoryScope)
			})
			if execErr != nil {
				content = "Tool execution failed: " + execErr.Error()
				failed = true
			} else {
				content = result
			}
		}
	case types.ApprovalStatusRejected:
		content = msgToolRejected
	case types.ApprovalStatusUnavailable:
		content = msgToolApprovalUnavailable
	case types.ApprovalStatusTimedOut:
		content = msgToolApprovalTimedOut
	default:
		return toolResult{}, fmt.Errorf("unexpected approval status %q for tool %q", approvalStatus, tc.ToolName)
	}

	emitToolComplete(emit, messageID, tc.ToolCallID, content)
	return toolResult{message: interfaces.Message{
		Role: interfaces.MessageRoleTool, Content: content,
		ToolName: tc.ToolName, ToolCallID: tc.ToolCallID,
	}, failed: failed, llmUsage: llmUsage}, nil
}

// awaitApproval creates a Restate awakeable, emits the approval CUSTOM event with the token,
// then blocks until approval arrives or the task timeout fires.
func (rt *RestateRuntime) awaitApproval(
	ctx restatesdk.Context,
	tc base.ToolCallRequest,
	input AgentLoopInput,
	emit func(events.AgentEvent),
) (types.ApprovalStatus, error) {
	awake, timeout := emitApprovalRequest(ctx, rt, tc, input, emit)
	first, waitErr := restatesdk.WaitFirst(ctx, awake, timeout)
	if waitErr != nil {
		return "", waitErr
	}
	if first == timeout {
		if err := timeout.Done(); err != nil {
			return "", err
		}
		return types.ApprovalStatusTimedOut, nil
	}
	return awake.Result()
}

// emitApprovalRequest creates a Restate awakeable and emits the corresponding approval CUSTOM
// event with the token embedded. Shared by sequential (awaitApproval) and parallel paths.
func emitApprovalRequest(
	ctx restatesdk.Context,
	rt *RestateRuntime,
	tc base.ToolCallRequest,
	input AgentLoopInput,
	emit func(events.AgentEvent),
) (restatesdk.AwakeableFuture[types.ApprovalStatus], restatesdk.AfterFuture) {
	awakeable := restatesdk.Awakeable[types.ApprovalStatus](ctx)
	token := awakeable.Id()
	if _, isSubAgent := input.SubAgentRoutes[tc.ToolName]; isSubAgent {
		emit(events.NewAgentCustomEvent(string(events.AgentCustomEventNameSubAgentDelegation),
			events.AgentCustomEventDelegationValue{
				AgentName:     rt.AgentSpec.Name,
				SubAgentName:  tc.ToolDisplayName,
				Args:          tc.Args,
				ApprovalToken: token,
			}))
	} else {
		emit(events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval),
			events.AgentCustomEventApprovalValue{
				AgentName:       rt.AgentSpec.Name,
				ToolCallID:      tc.ToolCallID,
				ToolName:        tc.ToolName,
				ToolDisplayName: tc.ToolDisplayName,
				Args:            tc.Args,
				ApprovalToken:   token,
			}))
	}
	return awakeable, restatesdk.After(ctx, rt.approvalTaskTimeout())
}

// checkBudget adds usage to the tracker and, when enforceBudget is true, applies OnExceeded.
func (rt *RestateRuntime) checkBudget(
	ctx restatesdk.Context,
	tracker *base.BudgetTracker,
	enforceBudget bool,
	telemetry *types.AgentTelemetry,
	usage *interfaces.LLMUsage,
	emit func(events.AgentEvent),
) error {
	if !enforceBudget || tracker == nil {
		if tracker != nil {
			_ = tracker.Add(usage)
		}
		return nil
	}
	budgetErr := tracker.Add(usage)
	if budgetErr == nil {
		return nil
	}
	return rt.handleBudgetExceeded(ctx, tracker, telemetry, budgetErr, emit)
}

// handleBudgetExceeded applies OnExceeded: stop the run, or wait for approval then continue.
func (rt *RestateRuntime) handleBudgetExceeded(
	ctx restatesdk.Context,
	tracker *base.BudgetTracker,
	telemetry *types.AgentTelemetry,
	budgetErr error,
	emit func(events.AgentEvent),
) error {
	cfg := rt.AgentConfig.Limits.Budget
	action := types.BudgetStopRun
	if cfg != nil && cfg.OnExceeded != "" {
		action = cfg.OnExceeded
	}
	rt.logger.Warn(ctx, "restate: per-run budget exceeded",
		slog.String("scope", "loop"),
		slog.String("action", string(action)),
		slog.String("detail", budgetErr.Error()))

	if action == types.BudgetWaitForApproval {
		approved, waitErr := rt.awaitBudgetApproval(ctx, tracker, budgetErr.Error(), emit)
		if waitErr != nil {
			telemetry.Run.FinishReason = types.FinishReasonBudgetExceeded
			return fmt.Errorf("%w: %s", types.ErrBudgetExceeded, budgetErr.Error())
		}
		if approved {
			tracker.AdvanceWatermark()
			return nil
		}
	}
	telemetry.Run.FinishReason = types.FinishReasonBudgetExceeded
	return fmt.Errorf("%w: %s", types.ErrBudgetExceeded, budgetErr.Error())
}

// awaitBudgetApproval emits a CUSTOM budget_approval event and blocks on a Restate awakeable.
func (rt *RestateRuntime) awaitBudgetApproval(
	ctx restatesdk.Context,
	tracker *base.BudgetTracker,
	detail string,
	emit func(events.AgentEvent),
) (bool, error) {
	awakeable := restatesdk.Awakeable[types.ApprovalStatus](ctx)
	token := awakeable.Id()
	tokens, costUSD := tracker.Totals()
	emit(events.NewAgentCustomEvent(string(events.AgentCustomEventNameBudget), events.AgentCustomEventBudgetValue{
		AgentName:     rt.AgentSpec.Name,
		Detail:        detail,
		TotalTokens:   tokens,
		CostUSD:       costUSD,
		ApprovalToken: token,
	}))
	timeout := restatesdk.After(ctx, rt.approvalTaskTimeout())
	first, waitErr := restatesdk.WaitFirst(ctx, awakeable, timeout)
	if waitErr != nil {
		return false, waitErr
	}
	if first == timeout {
		if err := timeout.Done(); err != nil {
			return false, err
		}
		return false, nil
	}
	status, err := awakeable.Result()
	if err != nil {
		return false, err
	}
	return status == types.ApprovalStatusApproved, nil
}

// ─── Event emission ───────────────────────────────────────────────────────────

// emitEvent marshals ev and publishes it to eventLogService/topic via the handler context.
// Using Request (not Send) ensures the run cannot complete before the event is durable.
func (rt *RestateRuntime) emitEvent(ctx restatesdk.Context, eventLogService, topic string, eventTypes []events.AgentEventType, ev events.AgentEvent) {
	if ev == nil || !eventAllowed(eventTypes, ev) || strings.TrimSpace(topic) == "" {
		return
	}
	service := strings.TrimSpace(eventLogService)
	if service == "" {
		service = rt.eventLogServiceName
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		rt.logger.Warn(ctx, "restate: failed to marshal agent event",
			slog.String("scope", "loop"), slog.Any("error", err))
		return
	}
	if err := publishAgentEventJSON(ctx, service, topic, raw); err != nil {
		rt.logger.Warn(ctx, "restate: publish agent event failed",
			slog.String("scope", "loop"), slog.String("topic", topic),
			slog.String("eventLog", service), slog.Any("error", err))
	}
}

// emitEventIngress publishes ev via ingress Send (fire-and-forget) from inside a executeWithPolicy where
// only a plain context.Context is available. Each publish uses a fresh idempotency key.
//
// Note: calls inside executeWithPolicy are NOT re-executed on Restate journal replay — LLM stream token
// events are emitted exactly once on first execution; retries replay the step result from journal.
func (rt *RestateRuntime) emitEventIngress(ctx context.Context, eventLogService, topic string, eventTypes []events.AgentEventType, ev events.AgentEvent) {
	if rt.ingressClient == nil || ev == nil || !eventAllowed(eventTypes, ev) || strings.TrimSpace(topic) == "" {
		return
	}
	service := strings.TrimSpace(eventLogService)
	if service == "" {
		service = rt.eventLogServiceName
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, err = restateingress.ObjectSend[json.RawMessage](
		rt.ingressClient, service, topic, "Publish",
	).Send(ctx, raw, restatesdk.WithIdempotencyKey(uuid.New().String()))
	if err != nil {
		rt.logger.Warn(ctx, "restate: ingress publish agent event failed",
			slog.String("scope", "loop"), slog.String("topic", topic),
			slog.String("eventLog", service), slog.Any("error", err))
	}
}

// emitToolComplete emits TOOL_CALL_END then TOOL_CALL_RESULT for a completed tool call.
// Called identically from both sequential and parallel execution paths.
func emitToolComplete(emit func(events.AgentEvent), messageID, toolCallID, content string) {
	emit(events.NewAgentToolCallEndEvent(toolCallID))
	emit(events.NewAgentToolCallResultEvent(messageID, toolCallID, content, string(interfaces.MessageRoleTool)))
}

// eventAllowed reports whether ev's type is included in the eventTypes filter.
func eventAllowed(eventTypes []events.AgentEventType, ev events.AgentEvent) bool {
	if len(eventTypes) == 0 || ev == nil {
		return false
	}
	for _, et := range eventTypes {
		if et == events.AgentEventAll || et == ev.Type() {
			return true
		}
	}
	return false
}

// ─── Conversation persistence ─────────────────────────────────────────────────

// persistConversationMessages durably saves new messages to the conversation store.
// Errors are logged at Warn and do not abort the run.
func (rt *RestateRuntime) persistConversationMessages(
	ctx restatesdk.Context,
	conversationID string,
	messages []interfaces.Message,
	policy sdkruntime.ExecutionPolicy,
) {
	conv := rt.AgentConfig.Session.Conversation
	if conv == nil || len(messages) == 0 {
		return
	}
	if err := executeWithPolicyErr(ctx, "conversation-save", policy, func(runCtx restatesdk.RunContext) error {
		_, span := rt.Tracer.StartSpan(runCtx, "conversation.add_messages",
			interfaces.Attribute{Key: "conversation.id", Value: conversationID},
			interfaces.Attribute{Key: "message.count", Value: len(messages)},
		)
		defer span.End()
		for _, msg := range messages {
			if err := conv.AddMessage(runCtx, conversationID, msg); err != nil {
				rt.logger.Warn(runCtx, "restate: add conversation message failed",
					slog.String("scope", "loop"),
					slog.String("conversationID", conversationID),
					slog.Any("error", err))
			}
		}
		return nil
	}); err != nil {
		rt.logger.Warn(ctx, "restate: conversation save failed",
			slog.String("scope", "loop"),
			slog.String("conversationID", conversationID),
			slog.Any("error", err))
	}
}

// conversationMemoryEnabled reports whether conversation memory is enabled for this run.
func (rt *RestateRuntime) conversationMemoryEnabled(input AgentLoopInput) bool {
	return input.ConversationID != "" && rt.AgentConfig.Session.Conversation != nil
}

// ─── Durable step helpers ─────────────────────────────────────────────────────

// executeWithPolicy executes fn as a named Restate durable step with retry behaviour from policy.
func executeWithPolicy[T any](ctx restatesdk.Context, name string, policy sdkruntime.ExecutionPolicy, fn func(restatesdk.RunContext) (T, error)) (T, error) {
	out, err := restatesdk.Run(ctx, fn, policyToRunOpts(name, policy)...)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// executeWithPolicyErr is executeWithPolicy for steps that return only an error.
func executeWithPolicyErr(ctx restatesdk.Context, name string, policy sdkruntime.ExecutionPolicy, fn func(restatesdk.RunContext) error) error {
	_, err := executeWithPolicy(ctx, name, policy, func(runCtx restatesdk.RunContext) (struct{}, error) {
		return struct{}{}, fn(runCtx)
	})
	return err
}

// policyToRunOpts converts an ExecutionPolicy into Restate Run retry options.
func policyToRunOpts(name string, policy sdkruntime.ExecutionPolicy) []restatesdk.RunOption {
	opts := []restatesdk.RunOption{restatesdk.WithName(name)}
	if policy.MaxAttempts > 0 {
		opts = append(opts, restatesdk.WithMaxRetryAttempts(uint(policy.MaxAttempts)))
	}
	if policy.Retry.InitialInterval > 0 {
		opts = append(opts, restatesdk.WithInitialRetryInterval(policy.Retry.InitialInterval))
	}
	if policy.Retry.MaximumInterval > 0 {
		opts = append(opts, restatesdk.WithMaxRetryInterval(policy.Retry.MaximumInterval))
	}
	if policy.Retry.BackoffCoefficient > 0 {
		opts = append(opts, restatesdk.WithRetryIntervalFactor(float32(policy.Retry.BackoffCoefficient)))
	}
	if policy.Timeout > 0 && policy.MaxAttempts > 0 {
		opts = append(opts, restatesdk.WithMaxRetryDuration(time.Duration(policy.MaxAttempts)*policy.Timeout))
	}
	return opts
}
