package temporal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	agentrt "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/contrib/workflowstreams"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var (
	// Heartbeat for long LLM stream / tool execute: fail stuck attempts soon after worker loss (<< StartToClose).
	agentLongActivityHeartbeatTimeout  time.Duration = 30 * time.Second
	agentLongActivityHeartbeatInterval time.Duration = 10 * time.Second

	agentToolApprovalActivityMaxAttempts int32 = 1

	// AgentWorkflowCleanupActivity: short RPCs to CompleteActivityByID for leftover approvals.
	agentWorkflowCleanupActivityTaskTimeout time.Duration = 30 * time.Second
	agentWorkflowCleanupActivityMaxAttempts int32         = 3

	// publishStreamEventActivityTaskTimeout caps how long the one-shot event-forward activity
	// in sub-agent workflows may run. Kept short: the activity is a single signal + ack.
	publishStreamEventActivityTaskTimeout time.Duration = 15 * time.Second

	// AgentWorkflow uses ContinueAsNew when Temporal execution history crosses these bounds (see loop below).
	// Checked after each tool round (when tool results are appended). Not evaluated on the "LLM returned no tools"
	// exit path in the same iteration. Byte limit is tighter than the event pipeline because LLM payloads are large.
	agentWorkflowHistoryLength    = 10_000
	agentWorkflowHistorySizeBytes = 20_000_000
)

// User-facing tool results when approval is required.
const (
	msgToolRejected            = "Tool execution was rejected by the user."
	msgToolApprovalUnavailable = "Tool approval could not be completed because the event stream is unavailable; continuing without running the tool."
	msgToolApprovalTimedOut    = "Tool approval timed out; continuing without running the tool."
	msgToolUnauthorized        = "Tool execution was denied by authorization policy."
)

// AgentWorkflowInput is the input to AgentWorkflow.
//
// RootWorkflowID is empty for the top-level agent run (the root). Sub-agent child workflows set it
// to the root workflow's ID so their events are published to the root's WorkflowStream instead of
// a separate stream that the client is not subscribed to.
//
// StreamState carries the WorkflowStream's durable log across ContinueAsNew boundaries (root only).
// StreamingEnabled enables partial LLM token streaming (set by Agent.Stream).
// ConversationID is set when conversation is used; workflow fetches messages and writes via activities.
// SubAgentDepth is 0 for a top-level user run; each child workflow increments it (runtime cap vs maxSubAgentDepth).
// SubAgentRoutes maps sub-agent tool name -> route; built from WithSubAgents when the run starts.
// MemoryScope is resolved before the workflow starts and passed through for recall/store activities.
// EventTypes controls whether events are published to the stream: empty = no events, ["*"] = all events.
// AgentFingerprint is the per-run digest (config + resolved tools). Caller and worker compute it at resolve time.
type AgentWorkflowInput struct {
	UserPrompt       string                               `json:"user_prompt,omitempty"`
	RootWorkflowID   string                               `json:"root_workflow_id,omitempty"`
	StreamState      *workflowstreams.WorkflowStreamState `json:"stream_state,omitempty"`
	StreamingEnabled bool                                 `json:"streaming_enabled,omitempty"`
	ConversationID   string                               `json:"conversation_id,omitempty"`
	AgentFingerprint string                               `json:"agent_fingerprint,omitempty"`
	RunID            string                               `json:"run_id,omitempty"`
	MemoryScope      interfaces.MemoryScope               `json:"memory_scope,omitempty"`
	EventTypes       []events.AgentEventType              `json:"event_types,omitempty"`
	SubAgentDepth    int                                  `json:"sub_agent_depth,omitempty"`
	SubAgentRoutes   map[string]SubAgentRoute             `json:"sub_agent_routes,omitempty"`
	MaxSubAgentDepth int                                  `json:"max_sub_agent_depth,omitempty"`
	State            *AgentWorkflowState                  `json:"state,omitempty"`
}

// AgentWorkflowState is the state of the agent workflow.
// It is used to store the state of the agent workflow on continue-as-new.
type AgentWorkflowState struct {
	Iteration int                   `json:"iteration"`
	Messages  []interfaces.Message  `json:"messages"`
	LLMUsage  *interfaces.LLMUsage  `json:"llm_usage,omitempty"`
	Telemetry *types.AgentTelemetry `json:"telemetry,omitempty"`
}

// AgentRetrieverInput is the input to AgentRetrieverActivity.
type AgentRetrieverInput struct {
	AgentFingerprint string `json:"agent_fingerprint,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	UserPrompt       string `json:"user_prompt"`
}

// AgentRetrieverResult is the return value of AgentRetrieverActivity.
// RetrieverContext is the combined, formatted document context from all retrievers; empty when no
// documents were found. BuildLLMRequest injects it as a labeled "Relevant Context" message ahead
// of conversation history; the system prompt itself is left untouched.
type AgentRetrieverResult struct {
	RetrieverContext string `json:"retriever_context,omitempty"`
	TotalSearches    int64  `json:"total_searches,omitempty"`
	FailedSearches   int64  `json:"failed_searches,omitempty"`
}

// AgentMemoryRecallInput is the input to AgentMemoryRecallActivity.
type AgentMemoryRecallInput struct {
	AgentFingerprint string                 `json:"agent_fingerprint,omitempty"`
	RunID            string                 `json:"run_id,omitempty"`
	UserPrompt       string                 `json:"user_prompt"`
	MemoryScope      interfaces.MemoryScope `json:"memory_scope,omitempty"`
}

// AgentMemoryRecallResult is the return value of AgentMemoryRecallActivity.
type AgentMemoryRecallResult struct {
	MemoryContext string `json:"memory_context,omitempty"`
	TotalRecalls  int64  `json:"total_recalls,omitempty"`
	FailedRecalls int64  `json:"failed_recalls,omitempty"`
}

// AgentMemoryStoreInput is the input to AgentMemoryStoreActivity.
type AgentMemoryStoreInput struct {
	AgentFingerprint string                 `json:"agent_fingerprint,omitempty"`
	RunID            string                 `json:"run_id,omitempty"`
	MemoryScope      interfaces.MemoryScope `json:"memory_scope,omitempty"`
	Messages         []interfaces.Message   `json:"messages,omitempty"`
}

// AgentLLMInput is the input to AgentLLMActivity and AgentLLMStreamActivity.
// When ConversationID is set, the activity loads history from the store. MessageID is the assistant text id
// for TEXT_MESSAGE_* (and stream ordering with REASONING_*); the workflow sets it each turn.
// RetrieverContext is the pre-fetched RAG context from AgentRetrieverActivity (prefetch / hybrid modes).
// MemoryContext is the pre-fetched long-term memory context from AgentMemoryRecallActivity.
// StreamWorkflowID is the Temporal workflow ID of the stream the activity publishes events to;
// empty disables activity-side event publishing (non-streaming / non-approval runs).
type AgentLLMInput struct {
	AgentName        string               `json:"agent_name,omitempty"`
	ConversationID   string               `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message `json:"messages,omitempty"`
	SkipTools        bool                 `json:"skip_tools,omitempty"`
	AgentFingerprint string               `json:"agent_fingerprint,omitempty"`
	MessageID        string               `json:"message_id,omitempty"`
	StreamWorkflowID string               `json:"stream_workflow_id,omitempty"`
	MemoryContext    string               `json:"memory_context,omitempty"`
	RetrieverContext string               `json:"retriever_context,omitempty"`
	RunID            string               `json:"run_id,omitempty"`
	Iteration        int                  `json:"iteration,omitempty"`
}

// AgentLLMResult is the return value of AgentLLMActivity. Workflow uses it to decide: return content or execute tools.
type AgentLLMResult struct {
	Content    string               `json:"content"`
	ToolCalls  []ToolCallRequest    `json:"tool_calls"`
	Usage      *interfaces.LLMUsage `json:"usage,omitempty"`
	RetryCount int32                `json:"retry_count,omitempty"` // number of Temporal retries before this successful attempt (Attempt - 1)
}

// baseLLMResultToActivity converts a [base.LLMResult] (no JSON tags) to an [AgentLLMResult]
// (with JSON tags required for Temporal serialization). ToolCallRequests are copied field by field
// so the two types stay independent (temporal adds JSON tags, base does not).
func baseLLMResultToActivity(r *base.LLMResult) *AgentLLMResult {
	out := &AgentLLMResult{
		Content: r.Content,
		Usage:   base.CloneLLMUsage(r.Usage),
	}
	for _, tc := range r.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCallRequest{
			ToolCallID:      tc.ToolCallID,
			ToolName:        tc.ToolName,
			ToolDisplayName: tc.ToolDisplayName,
			ToolKind:        tc.ToolKind,
			Args:            tc.Args,
			NeedsApproval:   tc.NeedsApproval,
		})
	}
	return out
}

// ToolCallRequest is a tool invocation with approval flag. NeedsApproval is set by AgentLLMActivity.
type ToolCallRequest struct {
	ToolCallID      string         `json:"tool_call_id"` // from LLM; used to match tool results
	ToolName        string         `json:"tool_name"`
	ToolDisplayName string         `json:"tool_display_name,omitempty"`
	ToolKind        types.ToolKind `json:"tool_kind"`
	Args            map[string]any `json:"args"`
	NeedsApproval   bool           `json:"needs_approval"`
}

// QueryIsApprovalPending is the workflow query name that reports whether a ToolCallID still has a
// pending approval activity. Arg: toolCallID (string). Result: bool.
// Used by the forwarder on Events/reconnect to skip CUSTOM events for approvals that were already
// resolved while the subscriber was disconnected.
const QueryIsApprovalPending = "is_approval_pending"

// pendingApproval tracks one in-flight AgentToolApprovalActivity (keyed by ToolCallID in the map).
type pendingApproval struct {
	ActivityID string `json:"activity_id"`
}

// AgentWorkflowCleanupInput is the input to AgentWorkflowCleanupActivity.
// ApprovalActivityIDs are Temporal activity IDs of still-pending AgentToolApprovalActivity tasks.
type AgentWorkflowCleanupInput struct {
	ApprovalActivityIDs []string `json:"approval_activity_ids,omitempty"`
}

// agentToolCallInput bundles the workflow handle, per-iteration activity contexts, and emit plumbing for tool execution.
// Built once per sequential LLM tool round, or once per parallel branch (unique parallelSlot activity IDs).
type agentToolCallInput struct {
	wfCtx               workflow.Context
	input               AgentWorkflowInput
	messageID           string
	iteration           int
	streamWorkflowID    string // root stream owner; own ID for root, RootWorkflowID for sub-agents
	emitEvent           func(events.AgentEvent) error
	authorizeCtx        workflow.Context
	approvalCtx         workflow.Context
	authorizeActivityID string // set once in newAgentToolCallInput
	approvalActivityID  string // set once in newAgentToolCallInput
	executeActivityID   string // set once in newAgentToolCallInput
	policies            agentrt.ExecutionPolicies
	pendingApprovals    map[string]*pendingApproval // shared with AgentWorkflow; nil when not tracking
}

// agentToolCallOutput is the output of executeAgentToolCall.
type agentToolCallOutput struct {
	msg    interfaces.Message
	failed bool // true: hard err or ExecuteTool err
}

// agentToolResult is one tool outcome collected for the conversation and telemetry.
type agentToolResult struct {
	message interfaces.Message
	failed  bool
}

// AgentToolExecuteInput is the input to AgentToolExecuteActivity.
type AgentToolExecuteInput struct {
	ToolName         string                 `json:"tool_name"`
	Args             map[string]any         `json:"args"`
	ConversationID   string                 `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message   `json:"messages,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	RunID            string                 `json:"run_id,omitempty"`
	Iteration        int                    `json:"iteration,omitempty"`
	AgentFingerprint string                 `json:"agent_fingerprint,omitempty"`
	MemoryScope      interfaces.MemoryScope `json:"memory_scope,omitempty"`
}

// AgentToolApprovalInput is the input to AgentToolApprovalActivity.
// StreamWorkflowID is the workflow whose WorkflowStream receives the approval event;
// the activity publishes it via workflowstreams so the client can display the approval request.
type AgentToolApprovalInput struct {
	AgentName        string         `json:"agent_name"`
	ToolCallID       string         `json:"tool_call_id"`
	ToolName         string         `json:"tool_name"`
	ToolDisplayName  string         `json:"tool_display_name,omitempty"`
	Args             map[string]any `json:"args"`
	StreamWorkflowID string         `json:"stream_workflow_id"`
	SubAgentName     string         `json:"sub_agent_name,omitempty"`
	AgentFingerprint string         `json:"agent_fingerprint,omitempty"`
}

type AgentToolAuthorizeInput struct {
	ToolName         string         `json:"tool_name"`
	Args             map[string]any `json:"args"`
	ToolCallID       string         `json:"tool_call_id"`
	AgentFingerprint string         `json:"agent_fingerprint,omitempty"`
}

type AgentToolAuthorizeResult struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// AddConversationMessagesInput is the input to AddConversationMessagesActivity.
type AddConversationMessagesInput struct {
	ConversationID   string               `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message `json:"messages,omitempty"`
	AgentFingerprint string               `json:"agent_fingerprint,omitempty"`
}

// PublishStreamEventInput is the activity input for PublishStreamEventActivity.
// StreamWorkflowID is the Temporal workflow ID whose WorkflowStream should receive the event.
type PublishStreamEventInput struct {
	StreamWorkflowID string          `json:"stream_workflow_id"`
	EventJSON        json.RawMessage `json:"event_json"`
}

// AgentWorkflow runs the agent loop: LLM → tool calls → approval/execute → feed results back to LLM → repeat.
// Stops when LLM returns no tool calls, or max iterations reached.
//
// When input.RootWorkflowID is empty, this is the root workflow: it creates a WorkflowStream and publishes
// all agent events directly to it (no activity overhead). When non-empty, this is a sub-agent: events are
// forwarded to the root's stream via PublishStreamEventActivity.
//
// ContinueAsNew: when workflow history length or size (GetInfo) exceeds agentWorkflowHistory*, after tool
// results are merged into messages for that iteration. The root workflow detaches the stream's pollers,
// waits for all handlers to finish, captures the stream state, and carries it forward in StreamState.
func (rt *TemporalRuntime) AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (*types.AgentRunResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("workflow: agent run started", "scope", "workflow")
	if n := len(input.SubAgentRoutes); n > 0 {
		logger.Debug("workflow: sub-agent routes snapshot",
			"scope", "workflow",
			"routeCount", n,
			"subAgentDepth", input.SubAgentDepth)
	}

	agentName := rt.AgentSpec.Name
	model := rt.AgentConfig.LLM.Client.GetModel()
	maxIter := rt.AgentConfig.Limits.MaxIterations
	policies := rt.executionPolicies()

	// isRoot indicates this is the top-level workflow that owns the WorkflowStream.
	isRoot := input.RootWorkflowID == ""

	// streamWorkflowID is the Temporal workflow ID of the WorkflowStream that all events route to.
	// For root workflows: own ID. For sub-agents: the root's ID (passed in from the parent).
	var streamWorkflowID string
	if isRoot {
		streamWorkflowID = workflow.GetInfo(ctx).WorkflowExecution.ID
	} else {
		streamWorkflowID = input.RootWorkflowID
	}

	// stream is the root workflow's event log; nil for sub-agents (they publish to the root's stream).
	var stream *workflowstreams.WorkflowStream
	if isRoot {
		var err error
		stream, err = workflowstreams.NewWorkflowStream(ctx, input.StreamState)
		if err != nil {
			return nil, fmt.Errorf("workflow: create stream: %w", err)
		}
	}

	// pendingApprovals tracks in-flight AgentToolApprovalActivity invocations (ToolCallID → meta).
	// Exposed via QueryIsApprovalPending so the reconnect forwarder can skip already-resolved
	// CUSTOM approval events without re-prompting the user.
	pendingApprovals := make(map[string]*pendingApproval)
	if err := workflow.SetQueryHandler(ctx, QueryIsApprovalPending, func(toolCallID string) (bool, error) {
		_, ok := pendingApprovals[toolCallID]
		return ok, nil
	}); err != nil {
		return nil, fmt.Errorf("workflow: register %s query handler: %w", QueryIsApprovalPending, err)
	}

	var activityIDSuffix string
	err := workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		return uuid.New().String()
	}).Get(&activityIDSuffix)
	if err != nil {
		return nil, err
	}

	// On cancel (or other exit) while approvals are still pending, complete those async activities
	// so they do not stay Pending until StartToClose. Uses a disconnected context so cleanup
	// is not cancelled with the workflow. Entries are left in pendingApprovals on cancel
	// (see executeAgentToolCall) so this defer still sees them.
	defer func() {
		ids := pendingApprovalActivityIDs(pendingApprovals)
		if len(ids) == 0 {
			return
		}
		disconnectedCtx, _ := workflow.NewDisconnectedContext(ctx)
		cleanupCtx := workflow.WithActivityOptions(disconnectedCtx, workflow.ActivityOptions{
			ActivityID:          "AgentWorkflowCleanupActivity_" + activityIDSuffix,
			StartToCloseTimeout: agentWorkflowCleanupActivityTaskTimeout,
			RetryPolicy:         retryPolicy(agentWorkflowCleanupActivityMaxAttempts),
		})
		if cleanupErr := workflow.ExecuteActivity(cleanupCtx, rt.AgentWorkflowCleanupActivity, AgentWorkflowCleanupInput{
			ApprovalActivityIDs: ids,
		}).Get(cleanupCtx, nil); cleanupErr != nil {
			logger.Warn("workflow: cleanup activity failed",
				"scope", "workflow",
				"pendingApprovalCount", len(ids),
				"error", cleanupErr)
		}
	}()

	llmActCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.LLM, "AgentLLMActivity_"+activityIDSuffix, false))
	streamActCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.LLM, "AgentLLMStreamActivity_"+activityIDSuffix, true))

	// publishEventActCtx is used by sub-agent workflows to forward events to the root stream.
	// ActivityID is intentionally empty: Temporal auto-assigns unique IDs, which is safe for
	// concurrent coroutines (parallel tool branches each emit events independently).
	var publishEventActCtx workflow.Context
	if !isRoot {
		publishEventActCtx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: publishStreamEventActivityTaskTimeout,
			RetryPolicy:         retryPolicy(1), // single attempt; event loss is acceptable
		})
	}

	convCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.Conversation, "ConversationActivity_"+activityIDSuffix, false))
	retrieverActCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.Retriever, "AgentRetrieverActivity_"+activityIDSuffix, false))
	memoryActCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.Memory, "AgentMemoryActivity_"+activityIDSuffix, false))

	// emitAgentEvent publishes one event to the stream.
	// Root workflow: direct in-memory append (zero activity overhead).
	// Sub-agent workflow: delegates to PublishStreamEventActivity (one Temporal activity per event).
	//
	// EventTypes acts as a publish filter: if empty (non-streaming, no approval) no events are emitted.
	// ["*"] emits all event types; a specific list emits only the listed types.
	emitAgentEvent := func(wfCtx workflow.Context, ev events.AgentEvent) error {
		if ev == nil {
			return nil
		}
		eventTypes := input.EventTypes
		if len(eventTypes) == 0 {
			return nil
		}
		emit := false
		for _, et := range eventTypes {
			if et == events.AgentEventAll || et == ev.Type() {
				emit = true
				break
			}
		}
		if !emit {
			return nil
		}
		eventBytes, _ := ev.ToJSON()
		if isRoot {
			return stream.Topic(streamTopicEvents).Publish(json.RawMessage(eventBytes))
		}
		// Sub-agent: forward to root stream via activity.
		return workflow.ExecuteActivity(publishEventActCtx, rt.PublishStreamEventActivity, PublishStreamEventInput{
			StreamWorkflowID: streamWorkflowID,
			EventJSON:        eventBytes,
		}).Get(wfCtx, nil)
	}

	useStreaming := input.StreamingEnabled && rt.AgentConfig.LLM.Client.IsStreamSupported()

	// State restored after ContinueAsNew (iteration, messages, run telemetry).
	if input.State == nil {
		input.State = &AgentWorkflowState{
			Iteration: 0,
			Messages:  []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: input.UserPrompt}},
		}
	}
	if input.State.Telemetry == nil {
		input.State.Telemetry = base.NewAgentTelemetry(workflow.Now(ctx))
	}
	telemetry := input.State.Telemetry

	llmUsage := input.State.LLMUsage

	messages := input.State.Messages

	memoryContext := ""
	if rt.MemoryConfigured() && rt.RecallEnabled() {
		logger.Debug("workflow: memory recall started", "scope", "workflow")
		var memoryResult AgentMemoryRecallResult
		if err := workflow.ExecuteActivity(memoryActCtx, rt.AgentMemoryRecallActivity, AgentMemoryRecallInput{
			AgentFingerprint: input.AgentFingerprint,
			RunID:            input.RunID,
			UserPrompt:       input.UserPrompt,
			MemoryScope:      input.MemoryScope,
		}).Get(memoryActCtx, &memoryResult); err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			return nil, err
		}
		memoryContext = memoryResult.MemoryContext
		telemetry.Storage.TotalMemoryRecalls += memoryResult.TotalRecalls
		telemetry.Storage.FailedMemoryRecalls += memoryResult.FailedRecalls
		logger.Debug("workflow: memory recall done", "scope", "workflow", "hasContext", memoryContext != "")
	}

	// Pre-fetch retrieval context once before the first LLM call (prefetch and hybrid modes).
	// The resulting retrieverContext is forwarded to every AgentLLMInput in the run so the LLM always
	// sees the retrieved documents in its system prompt, regardless of the number of iterations.
	retrieverContext := ""
	retrieverMode := rt.AgentConfig.Retrievers.Mode
	if (retrieverMode == types.RetrieverModePrefetch || retrieverMode == types.RetrieverModeHybrid) &&
		len(rt.AgentConfig.Retrievers.Retrievers) > 0 {
		logger.Debug("workflow: retriever prefetch started", "scope", "workflow", "retrieverMode", string(retrieverMode), "retrieverCount", len(rt.AgentConfig.Retrievers.Retrievers))
		retrieverInput := AgentRetrieverInput{
			AgentFingerprint: input.AgentFingerprint,
			RunID:            input.RunID,
			UserPrompt:       input.UserPrompt,
		}
		var retrieverResult AgentRetrieverResult
		if err := workflow.ExecuteActivity(retrieverActCtx, rt.AgentRetrieverActivity, retrieverInput).Get(retrieverActCtx, &retrieverResult); err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			return nil, err
		}
		retrieverContext = retrieverResult.RetrieverContext
		telemetry.Storage.TotalRetrieverSearches += retrieverResult.TotalSearches
		telemetry.Storage.FailedRetrieverSearches += retrieverResult.FailedSearches
		telemetry.Storage.PrefetchSearches += retrieverResult.TotalSearches
		logger.Debug("workflow: retriever prefetch done", "scope", "workflow", "hasContext", retrieverContext != "")
	}

	lastContent := ""
	var llmResult AgentLLMResult
	for iter := input.State.Iteration; iter < maxIter; iter++ {

		messageID := uuid.New().String()

		llmInput := AgentLLMInput{
			AgentName:        agentName,
			ConversationID:   input.ConversationID,
			Messages:         messages,
			AgentFingerprint: input.AgentFingerprint,
			MessageID:        messageID,
			RunID:            input.RunID,
			Iteration:        iter,
			StreamWorkflowID: streamWorkflowID,
			MemoryContext:    memoryContext,
			RetrieverContext: retrieverContext,
		}

		if useStreaming {
			err = workflow.ExecuteActivity(streamActCtx, rt.AgentLLMStreamActivity, llmInput).Get(streamActCtx, &llmResult)
		} else {
			err = workflow.ExecuteActivity(llmActCtx, rt.AgentLLMActivity, llmInput).Get(llmActCtx, &llmResult)
		}
		if err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			return nil, err
		}

		telemetry.Run.TotalLLMCalls++
		telemetry.Run.LLMRetryCount += int64(llmResult.RetryCount)
		llmUsage = base.MergeLLMUsage(llmUsage, llmResult.Usage)

		if len(llmResult.ToolCalls) == 0 {
			// Final response: accumulate assistant message for conversation
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			lastContent = llmResult.Content
			break
		}

		if iter == maxIter-1 {
			logger.Info("workflow: max iterations reached, final LLM round without tools", "scope", "workflow", "iteration", iter)
			// Fresh messageID so the final SkipTools round does not reuse the tool-calls
			// round's ID (stream consumers / staleMessageIDs treat START after END as a new attempt).
			llmInput.MessageID = uuid.New().String()
			llmInput.SkipTools = true
			if useStreaming {
				err = workflow.ExecuteActivity(streamActCtx, rt.AgentLLMStreamActivity, llmInput).Get(streamActCtx, &llmResult)
			} else {
				err = workflow.ExecuteActivity(llmActCtx, rt.AgentLLMActivity, llmInput).Get(llmActCtx, &llmResult)
			}
			if err != nil {
				if temporal.IsCanceledError(err) {
					return nil, err
				}
				return nil, err
			}
			llmUsage = base.MergeLLMUsage(llmUsage, llmResult.Usage)
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			lastContent = llmResult.Content
			telemetry.Run.TotalLLMCalls++
			telemetry.Run.FinishReason = types.FinishReasonMaxIterations
			break
		}

		// Accumulate assistant message for next iteration
		assistantMsg := interfaces.Message{
			Role:      interfaces.MessageRoleAssistant,
			Content:   llmResult.Content,
			ToolCalls: make([]*interfaces.ToolCall, len(llmResult.ToolCalls)),
		}
		for i, tr := range llmResult.ToolCalls {
			assistantMsg.ToolCalls[i] = &interfaces.ToolCall{
				ToolCallID: tr.ToolCallID,
				ToolName:   tr.ToolName,
				Args:       tr.Args,
			}
		}
		messages = append(messages, assistantMsg)

		var toolResults []agentToolResult

		toolExecMode := rt.ToolExecutionMode
		if toolExecMode == "" {
			toolExecMode = types.AgentToolExecutionModeParallel
		}
		switch toolExecMode {
		case types.AgentToolExecutionModeParallel:
			{
				logger.Info("workflow: tool execution (parallel)",
					"scope", "workflow",
					"executionMode", string(types.AgentToolExecutionModeParallel),
					"toolCount", len(llmResult.ToolCalls))

				futures := make([]workflow.Future, len(llmResult.ToolCalls))
				for i := range llmResult.ToolCalls {
					i := i
					tc := llmResult.ToolCalls[i]
					logger.Debug("workflow: parallel tool branch scheduled",
						"scope", "workflow",
						"toolIndex", i,
						"toolName", tc.ToolName,
						"toolCallID", tc.ToolCallID)
					fut, settable := workflow.NewFuture(ctx)
					futures[i] = fut

					workflow.Go(ctx, func(gCtx workflow.Context) {
						gLog := workflow.GetLogger(gCtx)
						gLog.Debug("workflow: parallel tool branch started",
							"scope", "workflow",
							"toolIndex", i,
							"toolName", tc.ToolName,
							"toolCallID", tc.ToolCallID)
						slot := strconv.Itoa(i)
						parallelInput := rt.newAgentToolCallInput(gCtx, input, activityIDSuffix, messageID, iter, streamWorkflowID, emitAgentEvent, slot)
						parallelInput.pendingApprovals = pendingApprovals
						toolOutput, runErr := rt.executeAgentToolCall(parallelInput, tc)
						if runErr != nil {
							gLog.Debug("workflow: parallel tool branch finished with error",
								"scope", "workflow",
								"toolIndex", i,
								"toolName", tc.ToolName,
								"toolCallID", tc.ToolCallID,
								"error", runErr)
							settable.Set(nil, runErr)
							return
						}
						gLog.Debug("workflow: parallel tool branch finished ok",
							"scope", "workflow",
							"toolIndex", i,
							"toolName", tc.ToolName,
							"toolCallID", tc.ToolCallID)
						settable.Set(toolOutput, nil)
					})
				}

				toolResults = make([]agentToolResult, len(futures))
				for i, fut := range futures {
					tc := llmResult.ToolCalls[i]
					var v *agentToolCallOutput
					err := fut.Get(ctx, &v)

					if err != nil {
						logger.Debug("workflow: parallel tool future collected (error → synthetic tool message)",
							"scope", "workflow",
							"toolIndex", i,
							"toolName", tc.ToolName,
							"toolCallID", tc.ToolCallID,
							"error", err)
						toolResults[i] = agentToolResult{
							message: interfaces.Message{
								Role:       interfaces.MessageRoleTool,
								Content:    "Tool execution failed: " + err.Error(),
								ToolName:   tc.ToolName,
								ToolCallID: tc.ToolCallID,
							},
							failed: true,
						}
					} else {
						logger.Debug("workflow: parallel tool future collected (ok)",
							"scope", "workflow",
							"toolIndex", i,
							"toolName", tc.ToolName,
							"toolCallID", tc.ToolCallID)
						toolResults[i] = agentToolResult{
							message: v.msg,
							failed:  v.failed,
						}
					}
				}
			}
		case types.AgentToolExecutionModeSequential:
			{
				logger.Info("workflow: tool execution (sequential)",
					"scope", "workflow",
					"executionMode", string(types.AgentToolExecutionModeSequential),
					"toolCount", len(llmResult.ToolCalls))
				toolInput := rt.newAgentToolCallInput(ctx, input, activityIDSuffix, messageID, iter, streamWorkflowID, emitAgentEvent, "")
				toolInput.pendingApprovals = pendingApprovals
				toolResults = make([]agentToolResult, len(llmResult.ToolCalls))
				for i, tc := range llmResult.ToolCalls {
					logger.Debug("workflow: sequential tool executing",
						"scope", "workflow",
						"toolIndex", i,
						"toolName", tc.ToolName,
						"toolCallID", tc.ToolCallID,
						"toolCount", len(llmResult.ToolCalls))
					toolOutput, runErr := rt.executeAgentToolCall(toolInput, tc)
					if runErr != nil {
						logger.Info("workflow: sequential tool failed",
							"scope", "workflow",
							"toolIndex", i,
							"toolName", tc.ToolName,
							"toolCallID", tc.ToolCallID,
							"error", runErr)
						toolResults[i] = agentToolResult{
							message: interfaces.Message{
								Role:       interfaces.MessageRoleTool,
								Content:    "Tool execution failed: " + runErr.Error(),
								ToolName:   tc.ToolName,
								ToolCallID: tc.ToolCallID,
							},
							failed: true,
						}
						continue
					}
					logger.Debug("workflow: sequential tool completed",
						"scope", "workflow",
						"toolIndex", i,
						"toolName", tc.ToolName,
						"toolCallID", tc.ToolCallID)
					toolResults[i] = agentToolResult{
						message: toolOutput.msg,
						failed:  toolOutput.failed,
					}
				}
			}
		default:
			return nil, fmt.Errorf("invalid tool execution mode %q: use %q or %q", toolExecMode, types.AgentToolExecutionModeParallel, types.AgentToolExecutionModeSequential)
		}

		for i, result := range toolResults {
			tc := llmResult.ToolCalls[i]
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
			messages = append(messages, result.message)
		}

		if rt.conversationMemoryEnabled(input.ConversationID) && rt.AgentConfig.Session.ConversationSaveOnIteration && len(messages) > 0 {
			if err := workflow.ExecuteActivity(convCtx, rt.AddConversationMessagesActivity, AddConversationMessagesInput{
				ConversationID:   input.ConversationID,
				Messages:         messages,
				AgentFingerprint: input.AgentFingerprint,
			}).Get(convCtx, nil); err != nil {
				logger.Warn("workflow: persist conversation failed", "scope", "workflow", "conversationID", input.ConversationID, "messagesCount", len(messages), "error", err)
			} else {
				messages = []interfaces.Message{}
			}
		}

		// History-driven ContinueAsNew (same iteration boundary as tool results). Skipped when the LLM
		// returns no tools (final answer path breaks earlier in the loop).
		info := workflow.GetInfo(ctx)
		if info.GetCurrentHistoryLength() >= agentWorkflowHistoryLength || info.GetCurrentHistorySize() >= agentWorkflowHistorySizeBytes {
			logger.Info("workflow: history budget exceeded, continuing as new", "scope", "workflow",
				"iteration", iter+1,
				"messagesCount", len(messages),
				"historyLength", info.GetCurrentHistoryLength(),
				"historyLengthLimit", agentWorkflowHistoryLength,
				"historySizeBytes", info.GetCurrentHistorySize(),
				"historySizeBytesLimit", agentWorkflowHistorySizeBytes,
			)

			// Root workflow: detach stream pollers, wait for all handlers, capture stream state.
			// This ensures in-flight subscribers receive all items before the new run takes over.
			if isRoot {
				stream.DetachPollers()
				if awaitErr := workflow.Await(ctx, func() bool {
					return workflow.AllHandlersFinished(ctx)
				}); awaitErr != nil {
					return nil, awaitErr
				}
				streamState, stErr := stream.GetState(streamPublisherTTL)
				if stErr != nil {
					return nil, fmt.Errorf("workflow: stream get state for CAN: %w", stErr)
				}
				input.StreamState = streamState
			}

			input.State = &AgentWorkflowState{
				Iteration: iter + 1,
				Messages:  messages,
				LLMUsage:  llmUsage,
				Telemetry: telemetry,
			}
			return nil, workflow.NewContinueAsNewError(ctx, rt.AgentWorkflow, input)
		}
	}

	// Persist unsaved workflow messages. Flag off: full batch. Flag on: per-iteration saves cleared state; only the final assistant may remain.
	if rt.conversationMemoryEnabled(input.ConversationID) && len(messages) > 0 {
		if err := workflow.ExecuteActivity(convCtx, rt.AddConversationMessagesActivity, AddConversationMessagesInput{
			ConversationID:   input.ConversationID,
			Messages:         messages,
			AgentFingerprint: input.AgentFingerprint,
		}).Get(convCtx, nil); err != nil {
			logger.Warn("workflow: persist conversation failed", "scope", "workflow", "conversationID", input.ConversationID, "messagesCount", len(messages), "error", err)
			if !rt.AgentConfig.Session.ConversationSaveOnIteration {
				return nil, err
			}
		}
	}

	if rt.RunEndMemoryStoreEnabled() {
		if err := workflow.ExecuteActivity(memoryActCtx, rt.AgentMemoryStoreActivity, AgentMemoryStoreInput{
			AgentFingerprint: input.AgentFingerprint,
			RunID:            input.RunID,
			MemoryScope:      input.MemoryScope,
			Messages:         messages,
		}).Get(memoryActCtx, nil); err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			logger.Warn("workflow: memory store failed", "scope", "workflow", "error", err)
			telemetry.Storage.FailedMemoryStores++
		} else {
			telemetry.Storage.TotalMemoryStores++
		}
	}

	logger.Info("workflow: agent run completed", "scope", "workflow", "contentLen", len(lastContent))

	telemetry.Run.CompletedAt = workflow.Now(ctx)

	runResult := &types.AgentRunResult{
		Content:   lastContent,
		AgentName: agentName,
		Model:     model,
		Metadata:  map[string]any{},
		RunID:     input.RunID,
		LLMUsage:  llmUsage,
		Telemetry: telemetry,
	}

	return runResult, nil
}

func (rt *TemporalRuntime) conversationMemoryEnabled(conversationID string) bool {
	return conversationID != "" && rt.AgentConfig.Session.Conversation != nil
}

// newAgentToolCallInput builds activity contexts for one tool-call branch.
// parallelSlot must be unique across concurrent tools (e.g. index string); use empty when calls run sequentially.
func (rt *TemporalRuntime) newAgentToolCallInput(
	wfCtx workflow.Context,
	input AgentWorkflowInput,
	activityIDSuffix, messageID string,
	iteration int,
	streamWorkflowID string,
	emitAgentEvent func(workflow.Context, events.AgentEvent) error,
	parallelSlot string,
) agentToolCallInput {
	approvalTaskTimeout := rt.AgentConfig.Limits.ApprovalTimeout
	if approvalTaskTimeout == 0 {
		approvalTaskTimeout = types.MaxApprovalTimeout
	}
	if approvalTaskTimeout > types.MaxApprovalTimeout {
		approvalTaskTimeout = types.MaxApprovalTimeout
	}
	activityScope := activityIDSuffix
	if parallelSlot != "" {
		activityScope = activityIDSuffix + "_" + parallelSlot
	}
	authorizeActivityID := "AgentToolAuthorizeActivity_" + activityScope
	approvalActivityID := "AgentToolApprovalActivity_" + activityScope
	executeActivityID := "AgentToolExecuteActivity_" + activityScope
	policies := rt.executionPolicies()
	return agentToolCallInput{
		wfCtx:            wfCtx,
		input:            input,
		messageID:        messageID,
		iteration:        iteration,
		streamWorkflowID: streamWorkflowID,
		emitEvent: func(ev events.AgentEvent) error {
			return emitAgentEvent(wfCtx, ev)
		},
		authorizeCtx: workflow.WithActivityOptions(wfCtx, execActivityOptions(
			policies.ToolAuth, authorizeActivityID, false)),
		approvalCtx: workflow.WithActivityOptions(wfCtx, workflow.ActivityOptions{
			ActivityID:          approvalActivityID,
			StartToCloseTimeout: approvalTaskTimeout,
			RetryPolicy:         retryPolicy(agentToolApprovalActivityMaxAttempts),
		}),
		authorizeActivityID: authorizeActivityID,
		approvalActivityID:  approvalActivityID,
		executeActivityID:   executeActivityID,
		policies:            policies,
	}
}

// executeAgentToolCall runs authorize → approval → execute or sub-agent delegation for one tool call,
// emits tool stream events, and returns the tool role message for the conversation.
func (rt *TemporalRuntime) executeAgentToolCall(input agentToolCallInput, tc ToolCallRequest) (*agentToolCallOutput, error) {
	logger := workflow.GetLogger(input.wfCtx)
	agentName := rt.AgentSpec.Name

	emitToolEndThenResult := func(toolCallID, content string) error {
		if emitErr := input.emitEvent(events.NewAgentToolCallEndEvent(toolCallID)); emitErr != nil {
			return emitErr
		}
		return input.emitEvent(events.NewAgentToolCallResultEvent(input.messageID, toolCallID, content, string(interfaces.MessageRoleTool)))
	}

	if emitErr := input.emitEvent(events.NewAgentToolCallStartEvent(tc.ToolCallID, tc.ToolName, input.messageID)); emitErr != nil {
		return nil, emitErr
	}
	if argsJSON, err := json.Marshal(tc.Args); err == nil {
		s := strings.TrimSpace(string(argsJSON))
		if s != "" && s != "null" && s != "{}" {
			if emitErr := input.emitEvent(events.NewAgentToolCallArgsEvent(tc.ToolCallID, s)); emitErr != nil {
				return nil, emitErr
			}
		}
	}

	var authResult AgentToolAuthorizeResult
	authInput := AgentToolAuthorizeInput{
		ToolCallID:       tc.ToolCallID,
		ToolName:         tc.ToolName,
		Args:             tc.Args,
		AgentFingerprint: input.input.AgentFingerprint,
	}
	if err := workflow.ExecuteActivity(input.authorizeCtx, rt.AgentToolAuthorizeActivity, authInput).Get(input.authorizeCtx, &authResult); err != nil {
		return nil, err
	}
	if !authResult.Allowed {
		logger.Info("workflow: tool authorization denied", "scope", "workflow", "toolName", tc.ToolName, "toolCallID", tc.ToolCallID)
		content := msgToolUnauthorized
		if strings.TrimSpace(authResult.Reason) != "" {
			content = fmt.Sprintf("%s Reason: %s", content, authResult.Reason)
		}
		if emitErr := emitToolEndThenResult(tc.ToolCallID, content); emitErr != nil {
			return nil, emitErr
		}
		return &agentToolCallOutput{
			msg: interfaces.Message{
				Role:       interfaces.MessageRoleTool,
				Content:    content,
				ToolName:   tc.ToolName,
				ToolCallID: tc.ToolCallID,
			},
		}, nil
	}

	approvalStatus := types.ApprovalStatusApproved
	if tc.NeedsApproval {
		logger.Info("workflow: tool requires approval", "scope", "workflow", "toolName", tc.ToolName, "argCount", len(tc.Args))
		var status types.ApprovalStatus
		approvalInput := AgentToolApprovalInput{
			AgentName:        agentName,
			ToolCallID:       tc.ToolCallID,
			ToolName:         tc.ToolName,
			ToolDisplayName:  tc.ToolDisplayName,
			Args:             tc.Args,
			StreamWorkflowID: input.streamWorkflowID,
			AgentFingerprint: input.input.AgentFingerprint,
		}
		if route, ok := input.input.SubAgentRoutes[tc.ToolName]; ok {
			approvalInput.SubAgentName = route.Name
		}
		// Track this approval as pending so QueryIsApprovalPending reflects the current state.
		// Events/reconnect subscribers query this to skip CUSTOM events for already-resolved approvals.
		if input.pendingApprovals != nil {
			input.pendingApprovals[tc.ToolCallID] = &pendingApproval{
				ActivityID: input.approvalActivityID,
			}
		}
		err := workflow.ExecuteActivity(input.approvalCtx, rt.AgentToolApprovalActivity, approvalInput).Get(input.approvalCtx, &status)
		// Keep the map entry on cancel so AgentWorkflow's defer cleanup can CompleteActivityByID.
		// Delete on success, timeout, or other non-cancel errors (activity already terminal or unused).
		if input.pendingApprovals != nil && !temporal.IsCanceledError(err) {
			delete(input.pendingApprovals, tc.ToolCallID)
		}
		if err != nil {
			// StartToClose timeout (ApprovalTimeout): skip the tool via switch below.
			if temporal.IsTimeoutError(err) {
				logger.Warn("workflow: approval timed out, continuing without running the tool",
					"scope", "workflow", "toolName", tc.ToolName, "toolCallID", tc.ToolCallID, "error", err)
				approvalStatus = types.ApprovalStatusTimedOut
			} else {
				return nil, err
			}
		} else {
			approvalStatus = status
		}
		if approvalStatus == types.ApprovalStatusUnavailable {
			logger.Warn("workflow: approval unavailable, treating as rejected", "scope", "workflow", "toolName", tc.ToolName)
		}
	}

	var content string
	failed := false
	switch approvalStatus {
	case types.ApprovalStatusApproved:
		if route, ok := input.input.SubAgentRoutes[tc.ToolName]; ok {
			logger.Info("workflow: executing sub-agent delegation",
				"scope", "workflow",
				"tool", tc.ToolName,
				"toolCallID", tc.ToolCallID,
				"childTaskQueue", strings.TrimSpace(route.TaskQueue),
				"subAgentDepth", input.input.SubAgentDepth)
			var subErr error
			content, subErr = rt.delegateToSubAgent(input.wfCtx, input.input, tc, route, input.emitEvent)
			if subErr != nil {
				return nil, subErr
			}
		} else {
			logger.Info("workflow: executing tool",
				"scope", "workflow",
				"tool", tc.ToolName,
				"toolCallID", tc.ToolCallID)
			var result string
			execInput := AgentToolExecuteInput{
				ToolName:         tc.ToolName,
				Args:             tc.Args,
				ConversationID:   input.input.ConversationID,
				ToolCallID:       tc.ToolCallID,
				RunID:            input.input.RunID,
				Iteration:        input.iteration,
				AgentFingerprint: input.input.AgentFingerprint,
				MemoryScope:      input.input.MemoryScope,
			}
			toolPolicy := rt.toolExecutionPolicy(tc.ToolKind, input.policies)
			execCtx := workflow.WithActivityOptions(input.wfCtx, execActivityOptions(
				toolPolicy, input.executeActivityID, true))
			errExec := workflow.ExecuteActivity(execCtx, rt.AgentToolExecuteActivity, execInput).Get(execCtx, &result)
			if errExec != nil {
				content = "Tool execution failed: " + errExec.Error()
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
		return nil, fmt.Errorf("workflow: unexpected approval status %q for tool %q", approvalStatus, tc.ToolName)
	}
	if emitErr := emitToolEndThenResult(tc.ToolCallID, content); emitErr != nil {
		return nil, emitErr
	}
	return &agentToolCallOutput{
		msg: interfaces.Message{
			Role:       interfaces.MessageRoleTool,
			Content:    content,
			ToolName:   tc.ToolName,
			ToolCallID: tc.ToolCallID,
		},
		failed: failed,
	}, nil
}

// startLongActivityHeartbeats records activity heartbeats until stop is called. Used for long-running
// activities so Temporal can fail the attempt soon after a worker process stops (heartbeat gap > HeartbeatTimeout).
func startLongActivityHeartbeats(ctx context.Context) (stop func()) {
	stopCh := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(agentLongActivityHeartbeatInterval)
		defer ticker.Stop()
		activity.RecordHeartbeat(ctx, nil)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, nil)
			}
		}
	}()
	return func() {
		once.Do(func() { close(stopCh) })
	}
}

// newActivityStreamClient creates a workflowstreams client for publishing events from an activity.
// Returns nil when streamWorkflowID is empty (non-streaming, non-approval run without a stream).
// The workflow always populates StreamWorkflowID in AgentLLMInput and AgentToolApprovalInput
// (root → own workflow ID; sub-agent → root workflow ID), so this path is only reached
// when the activity is invoked outside a workflow context (e.g., standalone tests).
func (rt *TemporalRuntime) newActivityStreamClient(ctx context.Context, streamWorkflowID string) *workflowstreams.Client {
	if streamWorkflowID == "" {
		return nil
	}
	return newStreamClient(rt.temporalClient, streamWorkflowID)
}

// AgentLLMStreamActivity streams LLM response tokens. Event order: optional reasoning block
// (REASONING_*), then TEXT_MESSAGE_START → TEXT_MESSAGE_CONTENT* → TEXT_MESSAGE_END.
// When input.ConversationID is set, fetches messages from conversation and prepends to workflow messages.
// Events are published to the WorkflowStream via workflowstreams.Client with batched signals.
func (rt *TemporalRuntime) AgentLLMStreamActivity(ctx context.Context, input AgentLLMInput) (*AgentLLMResult, error) {
	tools, err := rt.fetchTools(ctx)
	if err != nil {
		return nil, err
	}
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, tools); err != nil {
		return nil, err
	}
	stopHB := startLongActivityHeartbeats(ctx)
	defer stopHB()

	actLog := newActivityLogger(activity.GetLogger(ctx))
	agentName := strings.TrimSpace(input.AgentName)

	messages := input.Messages
	if rt.conversationMemoryEnabled(input.ConversationID) {
		convMessages, err := rt.FetchConversationMessages(ctx, actLog, input.ConversationID)
		if err != nil {
			return nil, err
		}
		messages = append(convMessages, messages...)
	}

	// Create stream client for token-level event publishing.
	sc := rt.newActivityStreamClient(ctx, input.StreamWorkflowID)
	if sc != nil {
		defer func() {
			// Final flush: ensures any buffered tokens are delivered before the activity returns.
			if closeErr := sc.Close(ctx); closeErr != nil {
				actLog.Warn(ctx, "stream flush on activity exit", "error", closeErr)
			}
		}()

		// On retry (Attempt > 1), publish a retry signal so the forwarder discards any partial
		// tokens that the prior failed attempt already delivered to subscribers. Must be sent
		// before any content events so subscribers can react before new tokens arrive.
		if info := activity.GetInfo(ctx); info.Attempt > 1 && input.MessageID != "" {
			actLog.Warn(ctx, "activity: LLM stream retrying, signalling stale token discard",
				"scope", "activity", "messageID", input.MessageID, "attempt", info.Attempt)
			retryBytes, _ := json.Marshal(streamRetrySignal{MessageID: input.MessageID})
			sc.Topic(streamTopicRetry).Publish(json.RawMessage(retryBytes), true)
		}
	}

	emit := func(ev events.AgentEvent) {
		if sc == nil || ev == nil {
			return
		}
		eventBytes, _ := ev.ToJSON()
		// forceFlush=false: batch tokens; the final Close() above flushes the remainder.
		sc.Topic(streamTopicEvents).Publish(json.RawMessage(eventBytes), false)
	}

	executeLLMInput := base.ExecuteLLMInput{
		Logger:           actLog,
		AgentName:        agentName,
		MessageID:        input.MessageID,
		RunID:            input.RunID,
		Iteration:        input.Iteration,
		Messages:         messages,
		SkipTools:        input.SkipTools,
		RetrieverContext: input.RetrieverContext,
		MemoryContext:    input.MemoryContext,
		Tools:            tools,
		Emit:             emit,
	}

	result, err := rt.ExecuteLLMStream(ctx, executeLLMInput)
	if err != nil {
		return nil, err
	}
	out := baseLLMResultToActivity(result)
	if attempt := activity.GetInfo(ctx).Attempt; attempt > 1 {
		out.RetryCount = attempt - 1
	}
	return out, nil
}

// AgentRetrieverActivity runs all configured retrievers in parallel using input.UserPrompt as the query,
// then returns a combined document context string for injection into the LLM system prompt.
// Called only for [types.RetrieverModePrefetch] and [types.RetrieverModeHybrid].
// Partial failures (some retrievers fail) are logged and skipped; if all retrievers fail, the activity
// returns an error so Temporal can retry per the retry policy.
func (rt *TemporalRuntime) AgentRetrieverActivity(ctx context.Context, input AgentRetrieverInput) (*AgentRetrieverResult, error) {
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, nil); err != nil {
		return nil, err
	}
	actLog := newActivityLogger(activity.GetLogger(ctx))
	res, err := rt.ExecuteRetrievers(ctx, base.ExecuteRetrieversInput{
		Logger:    actLog,
		RunID:     input.RunID,
		Iteration: 0,
		Query:     input.UserPrompt,
	})
	if err != nil {
		return nil, err
	}
	return &AgentRetrieverResult{
		RetrieverContext: res.Context,
		TotalSearches:    res.TotalSearches,
		FailedSearches:   res.FailedSearches,
	}, nil
}

// AgentMemoryRecallActivity loads scoped long-term memories and returns formatted prompt context.
func (rt *TemporalRuntime) AgentMemoryRecallActivity(ctx context.Context, input AgentMemoryRecallInput) (*AgentMemoryRecallResult, error) {
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, nil); err != nil {
		return nil, err
	}
	actLog := newActivityLogger(activity.GetLogger(ctx))
	res, err := rt.ExecuteMemoryRecall(ctx, base.ExecuteMemoryRecallInput{
		Logger:    actLog,
		RunID:     input.RunID,
		Iteration: 0,
		Scope:     input.MemoryScope,
		Query:     input.UserPrompt,
	})
	if err != nil {
		return nil, err
	}
	return &AgentMemoryRecallResult{
		MemoryContext: res.Context,
		TotalRecalls:  res.TotalRecalls,
		FailedRecalls: res.FailedRecalls,
	}, nil
}

// AgentMemoryStoreActivity extracts and persists long-term memories from the run.
func (rt *TemporalRuntime) AgentMemoryStoreActivity(ctx context.Context, input AgentMemoryStoreInput) error {
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, nil); err != nil {
		return err
	}
	actLog := newActivityLogger(activity.GetLogger(ctx))
	return rt.ExecuteMemoryStore(ctx, base.ExecuteMemoryStoreInput{
		Logger:    actLog,
		RunID:     input.RunID,
		Iteration: 0,
		Scope:     input.MemoryScope,
		Messages:  input.Messages,
	})
}

// AgentLLMActivity calls the LLM and returns content plus any tool calls.
// When input.ConversationID is set, fetches from store and adds assistant message on completion.
// Events (e.g. reasoning) are published to the WorkflowStream when StreamWorkflowID is set.
func (rt *TemporalRuntime) AgentLLMActivity(ctx context.Context, input AgentLLMInput) (*AgentLLMResult, error) {
	tools, err := rt.fetchTools(ctx)
	if err != nil {
		return nil, err
	}
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, tools); err != nil {
		return nil, err
	}
	actLog := newActivityLogger(activity.GetLogger(ctx))
	agentName := strings.TrimSpace(input.AgentName)

	messages := input.Messages
	if rt.conversationMemoryEnabled(input.ConversationID) {
		convMessages, err := rt.FetchConversationMessages(ctx, actLog, input.ConversationID)
		if err != nil {
			return nil, err
		}
		messages = append(convMessages, messages...)
	}

	sc := rt.newActivityStreamClient(ctx, input.StreamWorkflowID)
	if sc != nil {
		defer func() { _ = sc.Close(ctx) }()

		// On retry, discard partial events from the prior failed attempt on the subscriber side.
		if info := activity.GetInfo(ctx); info.Attempt > 1 && input.MessageID != "" {
			actLog.Warn(ctx, "activity: LLM retrying, signalling stale token discard",
				"scope", "activity", "messageID", input.MessageID, "attempt", info.Attempt)
			retryBytes, _ := json.Marshal(streamRetrySignal{MessageID: input.MessageID})
			sc.Topic(streamTopicRetry).Publish(json.RawMessage(retryBytes), true)
		}
	}

	emit := func(ev events.AgentEvent) {
		if sc == nil || ev == nil {
			return
		}
		eventBytes, _ := ev.ToJSON()
		sc.Topic(streamTopicEvents).Publish(json.RawMessage(eventBytes), true)
	}

	executeLLMInput := base.ExecuteLLMInput{
		Logger:           actLog,
		AgentName:        agentName,
		MessageID:        input.MessageID,
		RunID:            input.RunID,
		Iteration:        input.Iteration,
		Messages:         messages,
		SkipTools:        input.SkipTools,
		RetrieverContext: input.RetrieverContext,
		MemoryContext:    input.MemoryContext,
		Tools:            tools,
		Emit:             emit,
	}

	result, err := rt.ExecuteLLM(ctx, executeLLMInput)
	if err != nil {
		return nil, err
	}
	out := baseLLMResultToActivity(result)
	if attempt := activity.GetInfo(ctx).Attempt; attempt > 1 {
		out.RetryCount = attempt - 1
	}
	return out, nil
}

// AgentWorkflowCleanupActivity cancels leftover pending approval activities by ID.
// Invoked from AgentWorkflow defer via a disconnected context when the workflow exits with
// pendingApprovals still populated (typically cancel while waiting for user approval).
// Already-resolved / not-found activities are ignored; other errors are returned for retry.
func (rt *TemporalRuntime) AgentWorkflowCleanupActivity(ctx context.Context, input AgentWorkflowCleanupInput) error {
	if len(input.ApprovalActivityIDs) == 0 {
		return nil
	}
	if rt.temporalClient == nil {
		return fmt.Errorf("activity: AgentWorkflowCleanupActivity requires a Temporal client")
	}
	info := activity.GetInfo(ctx)
	logger := activity.GetLogger(ctx)
	for _, activityID := range input.ApprovalActivityIDs {
		if activityID == "" {
			continue
		}
		// CompleteActivityByID with CanceledError reports activity task canceled (not Rejected).
		err := rt.temporalClient.CompleteActivityByID(
			ctx,
			info.Namespace,
			info.WorkflowExecution.ID,
			info.WorkflowExecution.RunID,
			activityID,
			nil,
			temporal.NewCanceledError("workflow cancelled while approval pending"),
		)
		if err != nil {
			if isNotFoundError(err) {
				logger.Debug("activity: cleanup approval already resolved",
					"scope", "activity",
					"activityID", activityID)
				continue
			}
			return fmt.Errorf("activity: cleanup CompleteActivityByID %q: %w", activityID, err)
		}
		logger.Debug("activity: cleanup cancelled pending approval",
			"scope", "activity",
			"activityID", activityID)
	}
	return nil
}

// AgentToolApprovalActivity blocks until the driver completes it via CompleteActivity.
// Publishes a CUSTOM (tool_approval / delegation) event to the WorkflowStream so the client
// can display the approval UI and call StreamHandle.Approve with the task token.
// StreamWorkflowID is the workflow whose stream receives the approval event (root or sub-agent root).
func (rt *TemporalRuntime) AgentToolApprovalActivity(ctx context.Context, input AgentToolApprovalInput) (types.ApprovalStatus, error) {
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, nil); err != nil {
		return types.ApprovalStatusNone, err
	}
	logger := activity.GetLogger(ctx)
	logger.Debug("activity: tool approval started", "scope", "activity", "tool", input.ToolName)

	info := activity.GetInfo(ctx)
	taskTokenB64 := base64.StdEncoding.EncodeToString(info.TaskToken)

	agentEventName := events.AgentCustomEventNameToolApproval
	if input.SubAgentName != "" {
		agentEventName = events.AgentCustomEventNameSubAgentDelegation
	}

	var ev *events.AgentCustomEvent
	if agentEventName == events.AgentCustomEventNameToolApproval {
		logger.Debug("activity: approval is tool approval",
			"scope", "activity",
			"tool", input.ToolName,
			"mainAgent", rt.AgentSpec.Name)
		ev = events.NewAgentCustomEvent(string(agentEventName), events.AgentCustomEventApprovalValue{
			AgentName:       input.AgentName,
			ToolCallID:      input.ToolCallID,
			ToolName:        input.ToolName,
			ToolDisplayName: input.ToolDisplayName,
			Args:            input.Args,
			ApprovalToken:   taskTokenB64,
		})
	} else {
		logger.Debug("activity: approval is sub-agent delegation",
			"scope", "activity",
			"tool", input.ToolName,
			"subAgent", input.SubAgentName,
			"mainAgent", rt.AgentSpec.Name)
		ev = events.NewAgentCustomEvent(string(agentEventName), events.AgentCustomEventDelegationValue{
			AgentName:     input.AgentName,
			SubAgentName:  input.SubAgentName,
			ToolCallID:    input.ToolCallID,
			Args:          input.Args,
			ApprovalToken: taskTokenB64,
		})
	}

	eventBytes, err := ev.ToJSON()
	if err != nil {
		return types.ApprovalStatusNone, fmt.Errorf("activity: marshal approval event: %w", err)
	}

	// Publish the approval event to the stream so the subscriber (client) can display the approval UI.
	sc := rt.newActivityStreamClient(ctx, input.StreamWorkflowID)
	if sc == nil {
		return types.ApprovalStatusUnavailable, nil
	}
	// forceFlush=true: approval events must reach the subscriber immediately so the user sees the prompt.
	sc.Topic(streamTopicEvents).Publish(json.RawMessage(eventBytes), true)
	if closeErr := sc.Close(ctx); closeErr != nil {
		// Flush failed: the approval event may not reach the subscriber.
		// Return Unavailable so the workflow skips approval rather than hanging indefinitely.
		logger.Warn("activity: approval event flush failed, returning unavailable",
			"scope", "activity", "tool", input.ToolName, "error", closeErr)
		return types.ApprovalStatusUnavailable, nil
	}

	logger.Debug("activity: approval event published, pending driver completion",
		"scope", "activity", "tool", input.ToolName)
	return types.ApprovalStatusPending, activity.ErrResultPending
}

// AddConversationMessagesActivity adds messages to the conversation memory.
func (rt *TemporalRuntime) AddConversationMessagesActivity(ctx context.Context, input AddConversationMessagesInput) error {
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, nil); err != nil {
		return err
	}
	conversationID := input.ConversationID
	messages := input.Messages
	logger := activity.GetLogger(ctx)

	msgCount := len(messages)

	logger.Debug("activity: add conversation messages started", "scope", "activity", "conversationID", conversationID, "messagesCount", msgCount)

	if rt.AgentConfig.Session.Conversation == nil {
		return fmt.Errorf("conversation is not configured")
	}

	ctx, sp := rt.Tracer.StartSpan(ctx, "conversation.add_messages",
		interfaces.Attribute{Key: "conversation.id", Value: conversationID},
		interfaces.Attribute{Key: "message.count", Value: msgCount},
	)
	defer sp.End()

	failCount := 0
	for _, msg := range messages {
		if err := rt.AgentConfig.Session.Conversation.AddMessage(ctx, conversationID, msg); err != nil {
			failCount++
			msgCount--
			logger.Warn("activity: add conversation message failed", "scope", "activity", "conversationID", conversationID, "error", err)
		}
	}
	if failCount > 0 {
		sp.SetAttribute("failed.count", failCount)
	}

	logger.Debug("activity: add conversation messages completed", "scope", "activity", "conversationID", conversationID, "messagesCount", msgCount)
	return nil
}

// AgentToolExecuteActivity executes a tool by name and adds tool message to conversation when ConversationID is set.
func (rt *TemporalRuntime) AgentToolExecuteActivity(ctx context.Context, input AgentToolExecuteInput) (string, error) {
	tools, err := rt.fetchTools(ctx)
	if err != nil {
		return "", err
	}
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, tools); err != nil {
		return "", err
	}
	stopHB := startLongActivityHeartbeats(ctx)
	defer stopHB()
	actLog := newActivityLogger(activity.GetLogger(ctx))
	return rt.ExecuteTool(ctx, base.ExecuteToolInput{
		Logger:     actLog,
		Tools:      tools,
		ToolName:   input.ToolName,
		Args:       input.Args,
		ToolCallID: input.ToolCallID,
		RunID:      input.RunID,
		Iteration:  input.Iteration,
	}, input.MemoryScope)
}

// AgentToolAuthorizeActivity checks optional programmatic authorization before approval/execute.
func (rt *TemporalRuntime) AgentToolAuthorizeActivity(ctx context.Context, input AgentToolAuthorizeInput) (AgentToolAuthorizeResult, error) {
	tools, err := rt.fetchTools(ctx)
	if err != nil {
		return AgentToolAuthorizeResult{}, err
	}
	if err := rt.verifyAgentFingerprint(ctx, input.AgentFingerprint, tools); err != nil {
		return AgentToolAuthorizeResult{}, err
	}
	actLog := newActivityLogger(activity.GetLogger(ctx))
	authResult, err := rt.AuthorizeTool(ctx, actLog, tools, input.ToolName, input.Args)
	if err != nil {
		return AgentToolAuthorizeResult{}, err
	}
	return AgentToolAuthorizeResult{Allowed: authResult.Allowed, Reason: authResult.Reason}, nil
}

// PublishStreamEventActivity publishes one agent event to a remote WorkflowStream.
// Used by sub-agent workflows to forward their events to the root workflow's stream so that
// the subscribing client sees a unified event sequence across the delegation tree.
// A new workflowstreams.Client is created per call with forceFlush=true to deliver immediately;
// the overhead is comparable to the prior SendAgentEventUpdateActivity approach.
func (rt *TemporalRuntime) PublishStreamEventActivity(ctx context.Context, in PublishStreamEventInput) error {
	if len(in.EventJSON) == 0 || in.StreamWorkflowID == "" {
		return nil
	}
	logger := activity.GetLogger(ctx)
	evType, _ := events.EventTypeFromJSON(in.EventJSON)
	logger.Debug("activity: publish stream event", "scope", "activity",
		"streamWorkflowID", in.StreamWorkflowID, "eventType", evType)

	sc := newStreamClient(rt.temporalClient, in.StreamWorkflowID)
	// forceFlush=true sends immediately without waiting for the batch interval.
	sc.Topic(streamTopicEvents).Publish(json.RawMessage(in.EventJSON), true)
	if err := sc.Close(ctx); err != nil {
		logger.Warn("activity: publish stream event flush failed", "scope", "activity",
			"streamWorkflowID", in.StreamWorkflowID, "eventType", evType, "error", err)
		// Non-fatal: event loss is preferable to failing the workflow tool round.
	}
	return nil
}

// delegateToSubAgent runs a child AgentWorkflow for one sub-agent tool call and returns its text content.
// RootWorkflowID is propagated so the child workflow's events reach the root's WorkflowStream.
func (rt *TemporalRuntime) delegateToSubAgent(ctx workflow.Context, input AgentWorkflowInput, tc ToolCallRequest, route SubAgentRoute, emitEvent func(events.AgentEvent) error) (string, error) {
	logger := workflow.GetLogger(ctx)
	if strings.TrimSpace(route.TaskQueue) == "" {
		logger.Warn("workflow: sub-agent delegation skipped (empty task queue)",
			"scope", "workflow",
			"tool", tc.ToolName,
			"toolCallID", tc.ToolCallID)
		return "Sub-agent delegation failed: sub-agent task queue is not configured.", nil
	}
	maxDepth := input.MaxSubAgentDepth
	if input.SubAgentDepth >= maxDepth {
		logger.Warn("workflow: sub-agent delegation refused (max depth)",
			"scope", "workflow",
			"subAgentDepth", input.SubAgentDepth,
			"maxDepth", maxDepth,
			"tool", tc.ToolName,
			"toolCallID", tc.ToolCallID)
		return fmt.Sprintf("Sub-agent delegation refused: maximum nesting depth (%d) reached for this agent.", maxDepth), nil
	}

	query := base.SubAgentQuery(tc.Args)
	subAgentID := strings.TrimSpace(route.Name)
	if subAgentID == "" {
		subAgentID = tc.ToolName
	}

	var childSuffix string
	if err := workflow.SideEffect(ctx, func(workflow.Context) interface{} {
		return uuid.New().String()
	}).Get(&childSuffix); err != nil {
		logger.Warn("workflow: sub-agent child run id failed", "scope", "workflow", "error", err)
		return "", err
	}

	// rootWorkflowIDForChild is the stream owner for the child's events.
	// If this (the parent) is the root, use the own workflow ID.
	// If this is already a sub-agent, propagate its root ID unchanged.
	rootWorkflowIDForChild := input.RootWorkflowID
	if rootWorkflowIDForChild == "" {
		rootWorkflowIDForChild = workflow.GetInfo(ctx).WorkflowExecution.ID
	}

	childInput := AgentWorkflowInput{
		UserPrompt:       query,
		RunID:            childSuffix,
		RootWorkflowID:   rootWorkflowIDForChild,
		StreamingEnabled: input.StreamingEnabled,
		ConversationID:   "",
		AgentFingerprint: route.AgentFingerprint,
		EventTypes:       input.EventTypes,
		MemoryScope:      base.SubAgentScope(input.MemoryScope, subAgentID),
		SubAgentDepth:    input.SubAgentDepth + 1,
		SubAgentRoutes:   route.ChildRoutes,
		MaxSubAgentDepth: input.MaxSubAgentDepth,
	}

	parentID := workflow.GetInfo(ctx).WorkflowExecution.ID
	childWfID := fmt.Sprintf("%s-sub-%s-%s", parentID, tc.ToolCallID, childSuffix)
	childTO := rt.subAgentChildWorkflowTimeout()

	logger.Debug("workflow: sub-agent child run starting",
		"scope", "workflow",
		"childWorkflowID", childWfID,
		"childTaskQueue", strings.TrimSpace(route.TaskQueue),
		"tool", tc.ToolName,
		"toolCallID", tc.ToolCallID,
		"parentSubAgentDepth", input.SubAgentDepth,
		"childSubAgentDepth", childInput.SubAgentDepth,
		"nestedChildRouteCount", len(route.ChildRoutes),
		"workflowExecutionTimeout", childTO,
		"delegatedQueryLen", len(query))

	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:               childWfID,
		TaskQueue:                route.TaskQueue,
		WorkflowExecutionTimeout: childTO,
		ParentClosePolicy:        enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
		WaitForCancellation:      true,
	})

	delegationName := strings.TrimSpace(route.Name)
	if delegationName == "" {
		delegationName = tc.ToolName
	}

	if emitErr := emitEvent(events.NewAgentStepStartedEvent(delegationName)); emitErr != nil {
		return "", emitErr
	}

	var childResult *types.AgentRunResult
	if err := workflow.ExecuteChildWorkflow(childCtx, rt.AgentWorkflow, childInput).Get(childCtx, &childResult); err != nil {
		logger.Warn("workflow: sub-agent child run failed",
			"scope", "workflow",
			"childWorkflowID", childWfID,
			"tool", tc.ToolName,
			"error", err)
		return "Sub-agent workflow failed: " + err.Error(), nil
	}

	if emitErr := emitEvent(events.NewAgentStepFinishedEvent(delegationName)); emitErr != nil {
		return "", emitErr
	}

	logger.Debug("workflow: sub-agent child run completed",
		"scope", "workflow",
		"childWorkflowID", childWfID,
		"tool", tc.ToolName,
		"resultContentLen", len(childResult.Content))

	return childResult.Content, nil
}

// subAgentChildWorkflowTimeout caps how long the main agent waits on a delegated sub-agent run.
// Derived from the resolved sub-agent execution policy; falls back to the agent run timeout when
// no explicit timeout override was configured.
func (rt *TemporalRuntime) subAgentChildWorkflowTimeout() time.Duration {
	timeout := rt.executionPolicies().SubAgent.Timeout
	if timeout == 0 && rt.AgentConfig.Limits.Timeout > 0 {
		return rt.AgentConfig.Limits.Timeout
	}
	return timeout
}

// executionPolicies merges the agent's ExecutionConfig overrides onto SDK defaults and converts them to
// fully populated ExecutionPolicy values for every agent loop operation.
func (rt *TemporalRuntime) executionPolicies() agentrt.ExecutionPolicies {
	return agentrt.ResolveExecutionPolicies(rt.AgentConfig.ExecutionConfigs)
}

// toolExecutionPolicy returns the execution policy for a tool execution operation based on tool kind.
// MCP and A2A tools use their dedicated policies; all other tools use the generic ToolExecute policy.
func (rt *TemporalRuntime) toolExecutionPolicy(kind types.ToolKind, policies agentrt.ExecutionPolicies) agentrt.ExecutionPolicy {
	switch kind {
	case types.ToolKindMCP:
		return policies.MCP
	case types.ToolKindA2A:
		return policies.A2A
	default:
		return policies.ToolExecute
	}
}

// execActivityOptions builds Temporal ActivityOptions from a resolved ExecutionPolicy.
// The StartToCloseTimeout is set to the policy Timeout; retries follow the policy's own backoff.
// When withHeartbeat is true, a HeartbeatTimeout is added so long-running activities are detected as lost.
func execActivityOptions(policy agentrt.ExecutionPolicy, activityID string, withHeartbeat bool) workflow.ActivityOptions {
	attempts := int32(policy.MaxAttempts)
	if attempts < 1 {
		attempts = 1
	}
	opts := workflow.ActivityOptions{
		ActivityID:          activityID,
		StartToCloseTimeout: policy.Timeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    policy.Retry.InitialInterval,
			BackoffCoefficient: policy.Retry.BackoffCoefficient,
			MaximumInterval:    policy.Retry.MaximumInterval,
			MaximumAttempts:    attempts,
		},
	}
	if withHeartbeat {
		opts.HeartbeatTimeout = agentLongActivityHeartbeatTimeout
	}
	return opts
}

// pendingApprovalActivityIDs returns non-empty ActivityIDs from pendingApprovals.
func pendingApprovalActivityIDs(pendingApprovals map[string]*pendingApproval) []string {
	if len(pendingApprovals) == 0 {
		return nil
	}
	ids := make([]string, 0, len(pendingApprovals))
	for _, p := range pendingApprovals {
		if p == nil || p.ActivityID == "" {
			continue
		}
		ids = append(ids, p.ActivityID)
	}
	return ids
}

// retryPolicy builds a Temporal *RetryPolicy with SDK default backoff.
// maxAttempts is clamped to a minimum of 1 so a zero value does not disable retries.
func retryPolicy(maxAttempts int32) *temporal.RetryPolicy {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	def := agentrt.DefaultRetryPolicy()
	return &temporal.RetryPolicy{
		InitialInterval:    def.InitialInterval,
		BackoffCoefficient: def.BackoffCoefficient,
		MaximumInterval:    def.MaximumInterval,
		MaximumAttempts:    maxAttempts,
	}
}
