package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/hooks"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/google/uuid"
)

const (
	msgToolRejected            = "Tool execution was rejected by the user."
	msgToolApprovalUnavailable = "Tool approval could not be completed because no approval handler is configured; continuing without running the tool."
	msgToolUnauthorized        = "Tool execution was denied by authorization policy."
)

// AgentLoopInput holds per-run execution inputs for one local agent run.
// Mirrors AgentWorkflowInput (Temporal) for in-process execution — same fields, same semantics.
// Static agent wiring lives on the runtime [base.Runtime.AgentConfig]; resolved tools are per-run on Tools.
type AgentLoopInput struct {
	UserPrompt       string
	ConversationID   string
	StreamingEnabled bool
	// Tools is the resolved tool list for this run.
	Tools []interfaces.Tool
	// ChannelName is the eventbus channel events are published to during this run.
	// Sub-agents receive the parent's ChannelName so their events go directly to the parent stream.
	// Empty = no event fanout.
	ChannelName string
	// EventTypes filters which events are published to ChannelName (same semantics as Temporal).
	// Empty = publish nothing; ["*"] = all types; a specific list = only those types.
	EventTypes []events.AgentEventType
	// ApprovalHandler is called when a tool requires human approval. May be nil (approval → unavailable).
	ApprovalHandler types.ApprovalHandler
	// SubAgentRoutes maps sub-agent tool name → local route. Built by the local runtime from
	// ExecuteRequest.SubAgents before executeAgentLoop is called. Mirrors AgentWorkflowInput.SubAgentRoutes.
	SubAgentRoutes map[string]subAgentRoute
	// SubAgentDepth is the current nesting depth (0 = top-level, 1 = direct sub-agent, etc.).
	SubAgentDepth int
	// MaxSubAgentDepth caps recursive delegation. Mirrors AgentWorkflowInput.MaxSubAgentDepth.
	MaxSubAgentDepth int
	// MemoryScope is resolved before the run and used for recall/store.
	MemoryScope interfaces.MemoryScope
	// RunID is the stable identifier for this agent run; passed to LLM hooks via [RunMeta].
	RunID string
	// BudgetTracker tracks per-run token and cost usage against the configured limits.
	// Nil when no budget is configured. Shared with nested subagent calls; subagent calls
	// set EnforceBudget=false so only the root run triggers OnExceeded.
	BudgetTracker *base.BudgetTracker
	// EnforceBudget controls whether the budget check runs after each LLM call.
	// True on root runs when WithBudget is configured; false on nested subagent calls
	// so the parent run's tracker accumulates child usage and enforces the limit.
	EnforceBudget bool
}

// AgentLoopResult is the outcome of a completed local agent run.
type AgentLoopResult struct {
	Content   string
	LLMUsage  *interfaces.LLMUsage
	Telemetry *types.AgentTelemetry
}

type toolResult struct {
	message interfaces.Message
	failed  bool // true: hard err, ExecuteTool err, or ctx cancel
}

// publishEventToChannel marshals ev and publishes it on channelName via the runtime eventbus.
// A nil eventbus, empty channel, or nil event is a no-op.
func (rt *LocalRuntime) publishEventToChannel(ctx context.Context, channelName string, ev events.AgentEvent) {
	if ev == nil || strings.TrimSpace(channelName) == "" || rt.eventbus == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		rt.logger.Warn(ctx, "local: failed to marshal agent event",
			slog.String("scope", "loop"),
			slog.Any("error", err))
		return
	}
	if err := rt.eventbus.Publish(ctx, channelName, data); err != nil {
		rt.logger.Warn(ctx, "local: failed to publish agent event",
			slog.String("scope", "loop"),
			slog.String("channel", channelName),
			slog.Any("error", err))
	}
}

// executeAgentLoop executes the full agent loop in-process using base.Runtime core methods.
// It mirrors the orchestration logic of AgentWorkflow but calls base methods directly
// instead of dispatching to Temporal activities.
// Events are published to rt.eventbus on input.ChannelName; callers subscribe to that channel.
func (rt *LocalRuntime) executeAgentLoop(ctx context.Context, input AgentLoopInput) (*AgentLoopResult, error) {
	log := rt.logger
	telemetry := base.NewAgentTelemetry(time.Now())
	agentName := rt.AgentSpec.Name
	model := rt.AgentConfig.LLM.Client.GetModel()

	ctx, span := rt.Tracer.StartSpan(ctx, "agent.loop",
		interfaces.Attribute{Key: "agent.name", Value: agentName},
		interfaces.Attribute{Key: "model", Value: model},
	)
	defer span.End()

	tools := input.Tools
	policies := rt.executionPolicies()

	maxIter := rt.AgentConfig.Limits.MaxIterations
	if maxIter <= 0 {
		maxIter = 10
	}

	toolExecMode := rt.ToolExecutionMode
	if toolExecMode == "" {
		toolExecMode = types.AgentToolExecutionModeParallel
	}

	// Warn when a budget is configured on a sub-agent run — the parent's budget applies and
	// this agent's budget will not be enforced for this invocation.
	if input.BudgetTracker != nil && !input.EnforceBudget && input.SubAgentDepth > 0 {
		log.Warn(ctx, "local: agent has WithBudget but is running as a sub-agent; "+
			"budget enforcement is disabled for this invocation — the parent run's budget applies",
			slog.String("scope", "loop"),
			slog.Int("sub_agent_depth", input.SubAgentDepth))
	}

	// Internal emit: publish events to the eventbus channel for this run.
	// EventTypes acts as a publish filter: empty = no events; ["*"] = all; otherwise only listed types.
	emit := func(ev events.AgentEvent) {
		if ev == nil || len(input.EventTypes) == 0 {
			return
		}
		allowed := false
		for _, et := range input.EventTypes {
			if et == events.AgentEventAll || et == ev.Type() {
				allowed = true
				break
			}
		}
		if !allowed {
			return
		}
		rt.publishEventToChannel(ctx, input.ChannelName, ev)
	}

	// Build initial message list from user prompt.
	messages := []interfaces.Message{
		{Role: interfaces.MessageRoleUser, Content: input.UserPrompt},
	}

	// Prepend conversation history when conversation memory is configured for this run.
	persistedMessageCount := 0
	if rt.conversationMemoryEnabled(input) {
		convMsgs, err := rt.FetchConversationMessages(ctx, log, input.ConversationID)
		if err != nil {
			log.Warn(ctx, "local: failed to load conversation history, continuing without it",
				slog.String("scope", "loop"),
				slog.String("conversationID", input.ConversationID),
				slog.Any("error", err))
		} else {
			messages = append(convMsgs, messages...)
			persistedMessageCount = len(convMsgs)
		}
	}

	// Pre-fetch long-term memory context when recall is enabled.
	memoryContext := ""
	if rt.MemoryConfigured() && rt.RecallEnabled() {
		log.Debug(ctx, "local: memory recall started", slog.String("scope", "loop"))
		res, err := executeWithPolicy(ctx, policies.Memory, func(attemptCtx context.Context) (*base.MemoryResult, error) {
			return rt.ExecuteMemoryRecall(attemptCtx, base.ExecuteMemoryRecallInput{
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
		log.Debug(ctx, "local: memory recall done",
			slog.String("scope", "loop"),
			slog.Bool("hasContext", memoryContext != ""))
	}

	// Pre-fetch retriever context for prefetch/hybrid modes.
	retrieverContext := ""
	retrieverMode := rt.AgentConfig.Retrievers.Mode
	if (retrieverMode == types.RetrieverModePrefetch || retrieverMode == types.RetrieverModeHybrid) &&
		len(rt.AgentConfig.Retrievers.Retrievers) > 0 {
		log.Debug(ctx, "local: retriever prefetch started",
			slog.String("scope", "loop"),
			slog.String("mode", string(retrieverMode)),
			slog.Int("retrieverCount", len(rt.AgentConfig.Retrievers.Retrievers)))
		res, err := executeWithPolicy(ctx, policies.Retriever, func(attemptCtx context.Context) (*base.RetrieverResult, error) {
			return rt.ExecuteRetrievers(attemptCtx, base.ExecuteRetrieversInput{
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
		log.Debug(ctx, "local: retriever prefetch done",
			slog.String("scope", "loop"),
			slog.Bool("hasContext", retrieverContext != ""))
	}

	var lastContent string
	var llmUsage *interfaces.LLMUsage

	for iter := 0; iter < maxIter; iter++ {
		messageID := uuid.New().String()
		log.Debug(ctx, "local: LLM call started",
			slog.String("scope", "loop"),
			slog.Int("iteration", iter),
			slog.Int("messageCount", len(messages)))

		var llmResult *base.LLMResult
		var err error
		executeLLMInput := base.ExecuteLLMInput{
			Logger:           log,
			AgentName:        agentName,
			MessageID:        messageID,
			RunID:            input.RunID,
			Iteration:        iter,
			Messages:         messages,
			SkipTools:        false,
			MemoryContext:    memoryContext,
			RetrieverContext: retrieverContext,
			Tools:            tools,
			Emit:             emit,
		}
		if input.StreamingEnabled {
			llmResult, err = executeWithPolicy(ctx, policies.LLM, func(attemptCtx context.Context) (*base.LLMResult, error) {
				return rt.ExecuteLLMStream(attemptCtx, executeLLMInput)
			})
		} else {
			llmResult, err = executeWithPolicy(ctx, policies.LLM, func(attemptCtx context.Context) (*base.LLMResult, error) {
				return rt.ExecuteLLM(attemptCtx, executeLLMInput)
			})
		}
		if err != nil {
			return nil, fmt.Errorf("llm call (iter %d): %w", iter, err)
		}

		telemetry.Run.TotalLLMCalls++
		llmUsage = base.MergeLLMUsage(llmUsage, llmResult.Usage)
		if budgetErr := rt.checkBudget(ctx, input, telemetry, llmResult.Usage); budgetErr != nil {
			return &AgentLoopResult{Content: lastContent, LLMUsage: llmUsage, Telemetry: telemetry}, budgetErr
		}

		// Final response: no tool calls → done.
		if len(llmResult.ToolCalls) == 0 {
			messages = append(messages, interfaces.Message{
				Role:    interfaces.MessageRoleAssistant,
				Content: llmResult.Content,
			})
			lastContent = llmResult.Content
			break
		}

		// Max iterations: re-run without tools for a final answer.
		if iter == maxIter-1 {
			log.Info(ctx, "local: max iterations reached, forcing final LLM call without tools",
				slog.String("scope", "loop"),
				slog.Int("iteration", iter))
			finalMessageID := uuid.New().String()
			executeLLMInput := base.ExecuteLLMInput{
				Logger:           log,
				AgentName:        agentName,
				MessageID:        finalMessageID,
				RunID:            input.RunID,
				Iteration:        iter,
				Messages:         messages,
				SkipTools:        true,
				MemoryContext:    memoryContext,
				RetrieverContext: retrieverContext,
				Tools:            tools,
				Emit:             emit,
			}
			if input.StreamingEnabled {
				llmResult, err = executeWithPolicy(ctx, policies.LLM, func(attemptCtx context.Context) (*base.LLMResult, error) {
					return rt.ExecuteLLMStream(attemptCtx, executeLLMInput)
				})
			} else {
				llmResult, err = executeWithPolicy(ctx, policies.LLM, func(attemptCtx context.Context) (*base.LLMResult, error) {
					return rt.ExecuteLLM(attemptCtx, executeLLMInput)
				})
			}
			if err != nil {
				return nil, fmt.Errorf("llm final call (iter %d): %w", iter, err)
			}
			llmUsage = base.MergeLLMUsage(llmUsage, llmResult.Usage)
			if budgetErr := rt.checkBudget(ctx, input, telemetry, llmResult.Usage); budgetErr != nil {
				return &AgentLoopResult{Content: lastContent, LLMUsage: llmUsage, Telemetry: telemetry}, budgetErr
			}
			messages = append(messages, interfaces.Message{
				Role:    interfaces.MessageRoleAssistant,
				Content: llmResult.Content,
			})
			lastContent = llmResult.Content
			telemetry.Run.TotalLLMCalls++
			telemetry.Run.FinishReason = types.FinishReasonMaxIterations
			break
		}

		// Append assistant message with tool call metadata for next iteration.
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

		// Execute tools according to the requested execution mode.
		var toolResults []toolResult
		switch toolExecMode {
		case types.AgentToolExecutionModeParallel:
			toolResults, err = rt.executeToolsParallel(ctx, input, messageID, iter, llmResult.ToolCalls, policies, emit)
		case types.AgentToolExecutionModeSequential:
			toolResults, err = rt.executeToolsSequential(ctx, input, messageID, iter, llmResult.ToolCalls, policies, emit)
		default:
			return nil, fmt.Errorf("invalid tool execution mode %q: use %q or %q",
				toolExecMode,
				types.AgentToolExecutionModeParallel,
				types.AgentToolExecutionModeSequential)
		}
		if err != nil {
			return nil, err
		}

		for idx, result := range toolResults {
			messages = append(messages, result.message)
			tc := llmResult.ToolCalls[idx]
			if tc.ToolKind.CountsTowardToolTelemetry() {
				telemetry.Tools.Record(tc.ToolName, result.failed)
			}
			if tc.ToolKind == types.ToolKindRetriever {
				telemetry.Storage.TotalRetrieverSearches++
				telemetry.Storage.AgenticSearches++
				if result.failed {
					telemetry.Storage.FailedRetrieverSearches++
				}
			}
			if tc.ToolName == types.SaveMemoryToolName {
				if result.failed {
					telemetry.Storage.FailedMemoryStores++
				} else {
					telemetry.Storage.TotalMemoryStores++
				}
			}
		}

		// Nested sub-agents accumulate into the shared tracker during their run.
		// Enforce here (like Temporal/Restate after tools), not only on the next parent LLM call.
		if budgetErr := rt.enforceBudgetIfExceeded(ctx, input, telemetry); budgetErr != nil {
			return &AgentLoopResult{Content: lastContent, LLMUsage: llmUsage, Telemetry: telemetry}, budgetErr
		}

		if rt.conversationMemoryEnabled(input) && rt.AgentConfig.Session.ConversationSaveOnIteration && len(messages) > persistedMessageCount {
			rt.persistConversationMessages(ctx, input.ConversationID, messages[persistedMessageCount:])
			persistedMessageCount = len(messages)
		}
	}

	// Persist unsaved messages: full run when ConversationSaveOnIteration is false; final assistant only when true.
	if rt.conversationMemoryEnabled(input) && len(messages) > persistedMessageCount {
		rt.persistConversationMessages(ctx, input.ConversationID, messages[persistedMessageCount:])
	}

	if rt.RunEndMemoryStoreEnabled() {
		if err := executeWithPolicyErr(ctx, policies.Memory, func(attemptCtx context.Context) error {
			return rt.ExecuteMemoryStore(attemptCtx, base.ExecuteMemoryStoreInput{
				Logger:    log,
				RunID:     input.RunID,
				Iteration: 0,
				Scope:     input.MemoryScope,
				Messages:  messages,
			})
		}); err != nil {
			log.Warn(ctx, "local: memory store failed", slog.String("scope", "loop"), slog.Any("error", err))
			telemetry.Storage.FailedMemoryStores++
		} else {
			telemetry.Storage.TotalMemoryStores++
		}
	}

	log.Info(ctx, "local: agent run completed",
		slog.String("scope", "loop"),
		slog.String("agentName", agentName),
		slog.String("model", model),
		slog.Int("contentLen", len(lastContent)))

	telemetry.Run.CompletedAt = time.Now()

	return &AgentLoopResult{
		Content:   lastContent,
		LLMUsage:  llmUsage,
		Telemetry: telemetry,
	}, nil
}

func (rt *LocalRuntime) conversationMemoryEnabled(input AgentLoopInput) bool {
	return input.ConversationID != "" && rt.AgentConfig.Session.Conversation != nil
}

// executeToolsParallel runs all tool calls concurrently and collects results in submission order.
// Errors from individual tools are returned as synthetic tool messages so the LLM can handle
// partial failures gracefully (same behaviour as the Temporal parallel branch).
func (rt *LocalRuntime) executeToolsParallel(
	ctx context.Context,
	input AgentLoopInput,
	messageID string,
	iteration int,
	toolCalls []base.ToolCallRequest,
	policies sdkruntime.ExecutionPolicies,
	emit func(events.AgentEvent),
) ([]toolResult, error) {
	rt.logger.Info(ctx, "local: tool execution (parallel)",
		slog.String("scope", "loop"),
		slog.Int("toolCount", len(toolCalls)))

	results := make([]toolResult, len(toolCalls))
	var wg sync.WaitGroup

	for i := range toolCalls {
		wg.Add(1)
		go func(idx int, tc base.ToolCallRequest) {
			defer wg.Done()
			result, err := rt.executeSingleTool(ctx, input, messageID, iteration, tc, policies, emit)
			if err != nil {
				rt.logger.Info(ctx, "local: parallel tool failed",
					slog.String("scope", "loop"),
					slog.Int("toolIndex", idx),
					slog.String("toolName", tc.ToolName),
					slog.Any("error", err))
				result.message = interfaces.Message{
					Role:       interfaces.MessageRoleTool,
					Content:    "Tool execution failed: " + err.Error(),
					ToolName:   tc.ToolName,
					ToolCallID: tc.ToolCallID,
				}
				result.failed = true
			}
			results[idx] = result
		}(i, toolCalls[i])
	}
	wg.Wait()

	return results, nil
}

// executeToolsSequential runs tool calls one at a time.
// Hard errors from individual tools become synthetic tool messages; the batch always continues.
func (rt *LocalRuntime) executeToolsSequential(
	ctx context.Context,
	input AgentLoopInput,
	messageID string,
	iteration int,
	toolCalls []base.ToolCallRequest,
	policies sdkruntime.ExecutionPolicies,
	emit func(events.AgentEvent),
) ([]toolResult, error) {
	rt.logger.Info(ctx, "local: tool execution (sequential)",
		slog.String("scope", "loop"),
		slog.Int("toolCount", len(toolCalls)))

	results := make([]toolResult, len(toolCalls))
	for idx, tc := range toolCalls {
		result, err := rt.executeSingleTool(ctx, input, messageID, iteration, tc, policies, emit)
		if err != nil {
			rt.logger.Info(ctx, "local: sequential tool failed",
				slog.String("scope", "loop"),
				slog.Int("toolIndex", idx),
				slog.String("toolName", tc.ToolName),
				slog.Any("error", err))

			result.message = interfaces.Message{
				Role:       interfaces.MessageRoleTool,
				Content:    "Tool execution failed: " + err.Error(),
				ToolName:   tc.ToolName,
				ToolCallID: tc.ToolCallID,
			}
			result.failed = true
		}
		results[idx] = result
	}
	return results, nil
}

// executeSingleTool runs the full lifecycle for one tool call:
// authorize → approval (if needed) → execute, emitting TOOL_CALL_* events throughout.
// The caller must pass the already-resolved execution policies to avoid recomputing them per tool call.
func (rt *LocalRuntime) executeSingleTool(
	ctx context.Context,
	input AgentLoopInput,
	messageID string,
	iteration int,
	tc base.ToolCallRequest,
	policies sdkruntime.ExecutionPolicies,
	emit func(events.AgentEvent),
) (toolResult, error) {
	log := rt.logger
	tools := input.Tools

	emitToolEndThenResult := func(toolCallID, content string) {
		emit(events.NewAgentToolCallEndEvent(toolCallID))
		emit(events.NewAgentToolCallResultEvent(messageID, toolCallID, content, string(interfaces.MessageRoleTool)))
	}

	// TOOL_CALL_START
	emit(events.NewAgentToolCallStartEvent(tc.ToolCallID, tc.ToolName, messageID))

	// TOOL_CALL_ARGS (only when non-trivial args)
	if argsJSON, err := json.Marshal(tc.Args); err == nil {
		s := string(argsJSON)
		if s != "" && s != "null" && s != "{}" {
			emit(events.NewAgentToolCallArgsEvent(tc.ToolCallID, s))
		}
	}

	// Authorization check.
	authResult, err := executeWithPolicy(ctx, policies.ToolAuth, func(attemptCtx context.Context) (base.AuthorizeResult, error) {
		return rt.AuthorizeTool(attemptCtx, log, tools, tc.ToolName, tc.Args)
	})
	if err != nil {
		return toolResult{
			message: interfaces.Message{},
			failed:  true,
		}, fmt.Errorf("tool authorization error for %q: %w", tc.ToolName, err)
	}
	if !authResult.Allowed {
		content := msgToolUnauthorized
		if authResult.Reason != "" {
			content = fmt.Sprintf("%s Reason: %s", content, authResult.Reason)
		}
		log.Info(ctx, "local: tool authorization denied",
			slog.String("scope", "loop"),
			slog.String("toolName", tc.ToolName),
			slog.String("reason", authResult.Reason))
		emitToolEndThenResult(tc.ToolCallID, content)
		return toolResult{
			message: interfaces.Message{
				Role:       interfaces.MessageRoleTool,
				Content:    content,
				ToolName:   tc.ToolName,
				ToolCallID: tc.ToolCallID,
			},
			failed: false,
		}, nil
	}

	// Determine whether this tool call is a sub-agent delegation.
	subAgentRoute, isSubAgent := input.SubAgentRoutes[tc.ToolName]

	// Approval gate when required.
	approvalStatus := types.ApprovalStatusApproved
	if tc.NeedsApproval {
		// No channel (non-streaming Execute) and no handler: skip approval.
		if input.ChannelName == "" && input.ApprovalHandler == nil {
			approvalStatus = types.ApprovalStatusUnavailable
		} else {
			approvalTimeout := rt.approvalTaskTimeout()
			approvalTimer := time.NewTimer(approvalTimeout)
			defer approvalTimer.Stop()

			// Generate a token and register a resolve channel so either path can unblock:
			//   - Streaming: caller receives CUSTOM event with token, calls StreamHandle.Approve
			//   - Non-streaming with handler: handler calls approvalReq.Respond(status) directly
			token := uuid.New().String()
			resultCh := make(chan types.ApprovalStatus, 1)
			rt.pendingApprovals.Store(token, resultCh)
			defer rt.pendingApprovals.Delete(token)

			respond := func(status types.ApprovalStatus) error { resultCh <- status; return nil }

			if isSubAgent {
				approvalReq := &types.ApprovalRequest{
					Name: types.ApprovalRequestNameSubAgent,
					Value: types.SubAgentDelegationApprovalRequestValue{
						AgentName:     rt.AgentSpec.Name,
						SubAgentName:  tc.ToolDisplayName,
						Args:          tc.Args,
						ApprovalToken: token,
					},
					Respond: respond,
				}
				emit(events.NewAgentCustomEvent(string(events.AgentCustomEventNameSubAgentDelegation),
					events.AgentCustomEventDelegationValue{
						AgentName:     rt.AgentSpec.Name,
						SubAgentName:  tc.ToolDisplayName,
						Args:          tc.Args,
						ApprovalToken: token,
					}))
				if input.ApprovalHandler != nil {
					approvalCtx, approvalCancel := context.WithTimeout(ctx, approvalTimeout)
					input.ApprovalHandler(approvalCtx, approvalReq)
					approvalCancel()
				}
			} else {
				approvalReq := &types.ApprovalRequest{
					Name: types.ApprovalRequestNameTool,
					Value: types.ToolApprovalRequestValue{
						AgentName:       rt.AgentSpec.Name,
						ToolCallID:      tc.ToolCallID,
						ToolName:        tc.ToolName,
						ToolDisplayName: tc.ToolDisplayName,
						Args:            tc.Args,
						ApprovalToken:   token,
					},
					Respond: respond,
				}
				emit(events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval),
					events.AgentCustomEventApprovalValue{
						AgentName:       rt.AgentSpec.Name,
						ToolCallID:      tc.ToolCallID,
						ToolName:        tc.ToolName,
						ToolDisplayName: tc.ToolDisplayName,
						Args:            tc.Args,
						ApprovalToken:   token,
					}))
				if input.ApprovalHandler != nil {
					approvalCtx, approvalCancel := context.WithTimeout(ctx, approvalTimeout)
					input.ApprovalHandler(approvalCtx, approvalReq)
					approvalCancel()
				}
			}
			// Streaming path: handler is nil; caller calls StreamHandle.Approve(token, status) → resultCh.

			select {
			case status := <-resultCh:
				approvalStatus = status
			case <-ctx.Done():
				return toolResult{
					message: interfaces.Message{},
					failed:  true,
				}, ctx.Err()
			case <-approvalTimer.C:
				return toolResult{
					message: interfaces.Message{},
					failed:  true,
				}, fmt.Errorf("tool approval timed out after %v", approvalTimeout)
			}
		}
	}

	var content string
	failed := false
	switch approvalStatus {
	case types.ApprovalStatusApproved:
		if isSubAgent {
			delegationName := strings.TrimSpace(subAgentRoute.name)
			if delegationName == "" {
				delegationName = tc.ToolName
			}
			if input.SubAgentDepth >= input.MaxSubAgentDepth {
				log.Warn(ctx, "local: sub-agent delegation refused (max depth)",
					slog.String("scope", "loop"),
					slog.Int("depth", input.SubAgentDepth),
					slog.Int("maxDepth", input.MaxSubAgentDepth),
					slog.String("toolName", tc.ToolName))
				content = fmt.Sprintf("Sub-agent delegation refused: maximum nesting depth (%d) reached.", input.MaxSubAgentDepth)
			} else if subAgentRoute.runtime != nil {
				query := base.SubAgentQuery(tc.Args)
				log.Info(ctx, "local: delegating to sub-agent",
					slog.String("scope", "loop"),
					slog.String("toolName", tc.ToolName),
					slog.String("delegationName", delegationName),
					slog.Int("depth", input.SubAgentDepth+1))
				emit(events.NewAgentStepStartedEvent(delegationName))
				subResult, execErr := executeWithPolicy(ctx, rt.subAgentExecutionPolicy(), func(attemptCtx context.Context) (*AgentLoopResult, error) {
					return subAgentRoute.runtime.executeAgentLoop(attemptCtx, AgentLoopInput{
						UserPrompt:       query,
						RunID:            uuid.New().String(),
						StreamingEnabled: input.StreamingEnabled,
						ChannelName:      input.ChannelName,
						EventTypes:       input.EventTypes,
						ApprovalHandler:  input.ApprovalHandler,
						MemoryScope:      base.SubAgentScope(input.MemoryScope, delegationName),
						SubAgentRoutes:   subAgentRoute.children,
						SubAgentDepth:    input.SubAgentDepth + 1,
						MaxSubAgentDepth: input.MaxSubAgentDepth,
						Tools:            subAgentRoute.tools,
						BudgetTracker:    input.BudgetTracker,
						EnforceBudget:    false,
					})
				})
				emit(events.NewAgentStepFinishedEvent(delegationName))
				if execErr != nil {
					if errors.Is(execErr, types.ErrBudgetExceeded) {
						// Nested child stopped after shared budget Add; parent enforces after tools.
						if subResult != nil && strings.TrimSpace(subResult.Content) != "" {
							content = subResult.Content
						} else {
							content = "Sub-agent stopped: per-run budget exceeded."
						}
					} else {
						content = "Sub-agent execution failed: " + execErr.Error()
					}
				} else {
					content = subResult.Content
				}
			} else {
				content = "Sub-agent delegation not available for this runtime."
			}
		} else {
			log.Info(ctx, "local: executing tool",
				slog.String("scope", "loop"),
				slog.String("tool", tc.ToolName),
				slog.String("toolCallID", tc.ToolCallID))
			toolPolicy := rt.toolExecutionPolicy(tc.ToolKind, policies)
			result, execErr := executeWithPolicy(ctx, toolPolicy, func(attemptCtx context.Context) (string, error) {
				return rt.ExecuteTool(attemptCtx, base.ExecuteToolInput{
					Logger:     log,
					Tools:      tools,
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
	default:
		return toolResult{
			message: interfaces.Message{},
			failed:  true,
		}, fmt.Errorf("unexpected approval status %q for tool %q", approvalStatus, tc.ToolName)
	}

	emitToolEndThenResult(tc.ToolCallID, content)
	return toolResult{
		message: interfaces.Message{
			Role:       interfaces.MessageRoleTool,
			Content:    content,
			ToolName:   tc.ToolName,
			ToolCallID: tc.ToolCallID,
		},
		failed: failed,
	}, nil
}

// persistConversationMessages stores messages in the conversation store.
// Logs per-message failures and continues; does not fail the run.
func (rt *LocalRuntime) persistConversationMessages(ctx context.Context, conversationID string, messages []interfaces.Message) {
	conv := rt.AgentConfig.Session.Conversation
	if conv == nil || len(messages) == 0 {
		return
	}

	ctx, span := rt.Tracer.StartSpan(ctx, "conversation.add_messages",
		interfaces.Attribute{Key: "conversation.id", Value: conversationID},
		interfaces.Attribute{Key: "message.count", Value: len(messages)},
	)
	defer span.End()

	failCount := 0
	conversationPolicy := rt.executionPolicies().Conversation
	_ = executeWithPolicyErr(ctx, conversationPolicy, func(attemptCtx context.Context) error {
		for _, msg := range messages {
			if err := conv.AddMessage(attemptCtx, conversationID, msg); err != nil {
				failCount++
				rt.logger.Warn(ctx, "local: add conversation message failed",
					slog.String("scope", "loop"),
					slog.String("conversationID", conversationID),
					slog.Any("error", err))
			}
		}
		return nil
	})
	if failCount > 0 {
		span.SetAttribute("failed.count", failCount)
	}
}

// executeWithPolicy runs the given operation under the rules of the supplied [ExecutionPolicy],
// mirroring Temporal activity semantics locally. Each attempt gets a child context whose deadline
// is policy.Timeout; the parent context cancelling short-circuits the loop immediately.
// Between attempts the function waits with exponential backoff (InitialInterval, BackoffCoefficient,
// MaximumInterval from policy.Retry). The last error is returned when all attempts are exhausted.
func executeWithPolicy[T any](ctx context.Context, policy sdkruntime.ExecutionPolicy, operation func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	attempts := policy.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	backoff := policy.Retry.InitialInterval

	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx := ctx
		var cancel context.CancelFunc
		if policy.Timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, policy.Timeout)
		}

		result, err := operation(attemptCtx)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			return result, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		if attempt >= attempts {
			break
		}

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(backoff):
		}

		next := time.Duration(float64(backoff) * policy.Retry.BackoffCoefficient)
		if next > policy.Retry.MaximumInterval {
			backoff = policy.Retry.MaximumInterval
		} else {
			backoff = next
		}
	}
	return zero, lastErr
}

// executeWithPolicyErr is a convenience wrapper around [executeWithPolicy] for operations that return only an error.
// Use this instead of executeWithPolicy when there is no meaningful return value.
func executeWithPolicyErr(ctx context.Context, policy sdkruntime.ExecutionPolicy, operation func(ctx context.Context) error) error {
	_, err := executeWithPolicy(ctx, policy, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, operation(ctx)
	})
	return err
}

// executionPolicies merges the agent's ExecutionConfig overrides onto SDK defaults and converts them to
// fully populated [ExecutionPolicy] values for every agent loop operation.
func (rt *LocalRuntime) executionPolicies() sdkruntime.ExecutionPolicies {
	return sdkruntime.ResolveExecutionPolicies(rt.AgentConfig.ExecutionConfigs)
}

// toolExecutionPolicy returns the execution policy for a tool execution operation based on tool kind.
// MCP and A2A tools use their dedicated policies; all other tools use the generic ToolExecute policy.
func (rt *LocalRuntime) toolExecutionPolicy(kind types.ToolKind, policies sdkruntime.ExecutionPolicies) sdkruntime.ExecutionPolicy {
	switch kind {
	case types.ToolKindMCP:
		return policies.MCP
	case types.ToolKindA2A:
		return policies.A2A
	default:
		return policies.ToolExecute
	}
}

// approvalTaskTimeout returns the effective approval deadline, clamped to [types.MaxApprovalTimeout].
// When ApprovalTimeout is not configured it defaults to MaxApprovalTimeout.
func (rt *LocalRuntime) approvalTaskTimeout() time.Duration {
	timeout := rt.AgentConfig.Limits.ApprovalTimeout
	if timeout == 0 {
		timeout = types.MaxApprovalTimeout
	}
	if timeout > types.MaxApprovalTimeout {
		timeout = types.MaxApprovalTimeout
	}
	return timeout
}

// subAgentExecutionPolicy returns the execution policy for a sub-agent delegation operation.
// The sub-agent policy is derived from the already-resolved execution policies; when its timeout
// is still zero (no agent or SDK override set one), the agent run timeout is used as the ceiling.
func (rt *LocalRuntime) subAgentExecutionPolicy() sdkruntime.ExecutionPolicy {
	policy := rt.executionPolicies().SubAgent
	if policy.Timeout == 0 && rt.AgentConfig.Limits.Timeout > 0 {
		policy.Timeout = rt.AgentConfig.Limits.Timeout
	}
	return policy
}

// checkBudget runs after each LLM call. Parent runs (EnforceBudget) add usage and apply
// OnExceeded. Nested sub-agents only accumulate into the shared tracker; on breach they
// return ErrBudgetExceeded so the child stops and the parent can enforce after tools.
func (rt *LocalRuntime) checkBudget(
	ctx context.Context,
	input AgentLoopInput,
	telemetry *types.AgentTelemetry,
	usage *interfaces.LLMUsage,
) error {
	if input.BudgetTracker == nil {
		return nil
	}
	if !input.EnforceBudget {
		if err := input.BudgetTracker.Add(usage); err != nil {
			return fmt.Errorf("%w: %s", types.ErrBudgetExceeded, err.Error())
		}
		return nil
	}
	budgetErr := input.BudgetTracker.Add(usage)
	if budgetErr == nil {
		return nil
	}
	return rt.applyBudgetExceeded(ctx, input, telemetry, budgetErr)
}

// enforceBudgetIfExceeded applies OnExceeded when the shared tracker is already over limits
// (e.g. after a nested sub-agent accumulated past the parent budget).
func (rt *LocalRuntime) enforceBudgetIfExceeded(
	ctx context.Context,
	input AgentLoopInput,
	telemetry *types.AgentTelemetry,
) error {
	if !input.EnforceBudget || input.BudgetTracker == nil {
		return nil
	}
	budgetErr := input.BudgetTracker.Check()
	if budgetErr == nil {
		return nil
	}
	return rt.applyBudgetExceeded(ctx, input, telemetry, budgetErr)
}

func (rt *LocalRuntime) applyBudgetExceeded(
	ctx context.Context,
	input AgentLoopInput,
	telemetry *types.AgentTelemetry,
	budgetErr error,
) error {
	cfg := rt.AgentConfig.Limits.Budget
	action := types.BudgetStopRun
	if cfg != nil && cfg.OnExceeded != "" {
		action = cfg.OnExceeded
	}

	var budgetExcErr *base.BudgetExceededError
	errors.As(budgetErr, &budgetExcErr)

	kind := types.BudgetExceededKindTokens
	var totalTokens int64
	var totalCostUSD float64
	if budgetExcErr != nil {
		kind = budgetExcErr.Kind
		totalTokens = budgetExcErr.TotalTokens
		totalCostUSD = budgetExcErr.TotalCostUSD
	} else {
		totalTokens, totalCostUSD = input.BudgetTracker.Totals()
	}

	approvalsExhausted := action == types.BudgetWaitForApproval && input.BudgetTracker.ApprovalsExhausted()
	approvalCount := input.BudgetTracker.ApprovalCount()

	kindAttr := interfaces.Attribute{Key: types.MetricAttrBudgetKind, Value: string(kind)}
	actionAttr := interfaces.Attribute{Key: types.MetricAttrBudgetAction, Value: string(action)}

	rt.Metrics.IncrementCounter(ctx, types.MetricBudgetExceeded, kindAttr, actionAttr)

	rt.logger.Warn(ctx, "local: per-run budget exceeded",
		slog.String("scope", "loop"),
		slog.String("action", string(action)),
		slog.String("budget.kind", string(kind)),
		slog.Int64("budget.total_tokens", int64(totalTokens)),
		slog.Float64("budget.total_cost_usd", totalCostUSD),
		slog.Int("budget.approval_count", approvalCount),
		slog.Bool("budget.approvals_exhausted", approvalsExhausted),
		slog.String("detail", budgetErr.Error()))

	hooks.RunOnBudgetExceeded(ctx, rt.AgentConfig.Hooks, hooks.OnBudgetExceededHookInput{
		RunMeta:            hooks.RunMeta{RunID: input.RunID},
		Kind:               kind,
		TotalTokens:        totalTokens,
		TotalCostUSD:       totalCostUSD,
		Action:             action,
		ApprovalCount:      approvalCount,
		ApprovalsExhausted: approvalsExhausted,
	})

	if action == types.BudgetWaitForApproval && !approvalsExhausted {
		rt.Metrics.IncrementCounter(ctx, types.MetricBudgetApprovalRequested, kindAttr)
		status := rt.awaitBudgetApprovalStatus(ctx, input, budgetErr.Error())
		switch status {
		case types.ApprovalStatusApproved:
			rt.Metrics.IncrementCounter(ctx, types.MetricBudgetApprovalApproved, kindAttr)
			input.BudgetTracker.AdvanceWatermark()
			return nil
		case types.ApprovalStatusTimedOut:
			rt.Metrics.IncrementCounter(ctx, types.MetricBudgetApprovalTimedOut, kindAttr)
		case types.ApprovalStatusUnavailable:
			rt.Metrics.IncrementCounter(ctx, types.MetricBudgetApprovalUnavailable, kindAttr)
			rt.logger.Warn(ctx, "local: budget approval unavailable (no subscriber); stopping run",
				slog.String("scope", "loop"))
			telemetry.Run.FinishReason = types.FinishReasonBudgetExceeded
			return fmt.Errorf("%w: %s", types.ErrBudgetApprovalUnavailable, budgetErr.Error())
		default:
			rt.Metrics.IncrementCounter(ctx, types.MetricBudgetApprovalRejected, kindAttr)
		}
	} else if action == types.BudgetWaitForApproval && approvalsExhausted {
		rt.Metrics.IncrementCounter(ctx, types.MetricBudgetApprovalExhausted, kindAttr)
		rt.logger.Warn(ctx, "local: budget approval limit exhausted; stopping run",
			slog.String("scope", "loop"),
			slog.Int("budget.max_approvals", cfg.MaxApprovals))
	}

	telemetry.Run.FinishReason = types.FinishReasonBudgetExceeded
	return fmt.Errorf("%w: %w", types.ErrBudgetExceeded, budgetErr)
}

// awaitBudgetApprovalStatus sends a CUSTOM budget_approval event and invokes the approval
// handler. Returns the resolved ApprovalStatus. Returns ApprovalStatusUnavailable when
// there is no subscriber connected (channel empty and no handler configured).
func (rt *LocalRuntime) awaitBudgetApprovalStatus(ctx context.Context, input AgentLoopInput, detail string) types.ApprovalStatus {
	token := uuid.New().String()
	resultCh := make(chan types.ApprovalStatus, 1)
	respond := func(status types.ApprovalStatus) error {
		select {
		case resultCh <- status:
		default:
		}
		return nil
	}

	tokens, costUSD := input.BudgetTracker.Totals()
	value := types.BudgetApprovalRequestValue{
		AgentName:     rt.AgentSpec.Name,
		Detail:        detail,
		TotalTokens:   tokens,
		CostUSD:       costUSD,
		ApprovalToken: token,
	}
	approvalReq := &types.ApprovalRequest{
		Name:    types.ApprovalRequestNameBudget,
		Value:   value,
		Respond: respond,
	}

	// Register the token so streaming callers can call Stream.Approve(token, status) to unblock.
	rt.pendingApprovals.Store(token, resultCh)
	defer rt.pendingApprovals.Delete(token)

	// Emit CUSTOM event for streaming callers so they can call Stream.Approve(token, status).
	hasChannel := strings.TrimSpace(input.ChannelName) != ""
	rt.publishEventToChannel(ctx, input.ChannelName, events.NewAgentCustomEvent(string(events.AgentCustomEventNameBudget),
		events.AgentCustomEventBudgetValue{
			AgentName:     value.AgentName,
			Detail:        value.Detail,
			TotalTokens:   value.TotalTokens,
			CostUSD:       value.CostUSD,
			ApprovalToken: value.ApprovalToken,
		}))

	// Non-streaming: call the approval handler synchronously.
	if input.ApprovalHandler != nil {
		approvalTimeout := rt.approvalTaskTimeout()
		approvalCtx, cancel := context.WithTimeout(ctx, approvalTimeout)
		input.ApprovalHandler(approvalCtx, approvalReq)
		cancel()
	} else if !hasChannel {
		// No handler and no stream channel — cannot deliver the approval request.
		return types.ApprovalStatusUnavailable
	}

	select {
	case status := <-resultCh:
		return status
	case <-ctx.Done():
		return types.ApprovalStatusRejected
	}
}
