package temporal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var (
	agentLLMActivityTaskTimeout time.Duration = 30 * time.Minute
	agentLLMActivityMaxAttempts int32         = 3

	agentToolApprovalActivityMaxAttempts int32 = 3

	agentToolExecuteActivityTaskTimeout time.Duration = 30 * time.Minute
	agentToolExecuteActivityMaxAttempts int32         = 3

	sendEventActivityTaskTimeout time.Duration = 15 * time.Second
	sendEventActivityMaxAttempts int32         = 3

	conversationActivityTaskTimeout time.Duration = 30 * time.Second
	conversationActivityMaxAttempts int32         = 1
)

// AgentWorkflowInput is the input to AgentWorkflow. EventWorkflowID is set when streaming or approval is used.
// StreamingEnabled enables partial content streaming (from WithStream).
// ConversationID is set when conversation is used; workflow fetches messages and writes assistant/tool via activities.
// SubAgentDepth is 0 for a top-level user run; each child workflow increments it (runtime cap vs maxSubAgentDepth).
// SubAgentRoutes maps sub-agent tool name -> route; built from WithSubAgents when the run starts.
// LocalChannelName is the in-process pub/sub channel name used for in-memory event fan-in across the
// delegation tree. Set once at the top level (agent_event_<main-workflow-id>) and propagated unchanged
// to all sub-agents. Contrast with EventWorkflowID which is used for out-of-process (remote) routing.
// EventTypes is set by the SDK; a single "*" element means emit all event kinds (used for Stream).
// AgentFingerprint is the SHA-256 hex digest of the worker-local agent config; activities reject on mismatch.
type AgentWorkflowInput struct {
	UserPrompt       string                         `json:"user_prompt,omitempty"`
	EventWorkflowID  string                         `json:"event_workflow_id,omitempty"`
	LocalChannelName string                         `json:"local_channel_name,omitempty"`
	StreamingEnabled bool                           `json:"streaming_enabled,omitempty"`
	ConversationID   string                         `json:"conversation_id,omitempty"`
	AgentFingerprint string                         `json:"agent_fingerprint,omitempty"`
	EventTypes       []types.AgentEventType         `json:"event_types,omitempty"`
	SubAgentDepth    int                            `json:"sub_agent_depth,omitempty"`
	SubAgentRoutes   map[string]types.SubAgentRoute `json:"sub_agent_routes,omitempty"`
	MaxSubAgentDepth int                            `json:"max_sub_agent_depth,omitempty"`
}

// AgentLLMInput is the input to AgentLLMActivity. When ConversationID is set, activity fetches messages from store.
// UserPrompt is passed directly; no message construction in workflow. Messages used only for non-conversation multi-turn.
type AgentLLMInput struct {
	ConversationID   string               `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message `json:"messages,omitempty"`
	SkipTools        bool                 `json:"skip_tools,omitempty"`
	AgentFingerprint string               `json:"agent_fingerprint,omitempty"`
}

// AgentLLMStreamInput is the input to AgentLLMStreamActivity.
type AgentLLMStreamInput struct {
	AgentName        string               `json:"agent_name,omitempty"`
	ConversationID   string               `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message `json:"messages,omitempty"`
	EventWorkflowID  string               `json:"event_workflow_id,omitempty"`
	LocalChannelName string               `json:"local_channel_name,omitempty"`
	SkipTools        bool                 `json:"skip_tools,omitempty"`
	AgentFingerprint string               `json:"agent_fingerprint,omitempty"`
}

// AgentLLMResult is the return value of AgentLLMActivity. Workflow uses it to decide: return content or execute tools.
type AgentLLMResult struct {
	Content   string            `json:"content"`
	ToolCalls []ToolCallRequest `json:"tool_calls"`
}

// ToolCallRequest is a tool invocation with approval flag. NeedsApproval is set by AgentLLMActivity.
type ToolCallRequest struct {
	ToolCallID    string         `json:"tool_call_id"` // from LLM; used to match tool results
	ToolName      string         `json:"tool_name"`
	Args          map[string]any `json:"args"`
	NeedsApproval bool           `json:"needs_approval"`
}

// AgentToolExecuteInput is the input to AgentToolExecuteActivity.
type AgentToolExecuteInput struct {
	ToolName         string               `json:"tool_name"`
	Args             map[string]any       `json:"args"`
	ConversationID   string               `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message `json:"messages,omitempty"`
	ToolCallID       string               `json:"tool_call_id,omitempty"`
	AgentFingerprint string               `json:"agent_fingerprint,omitempty"`
}

type AgentToolApprovalInput struct {
	ToolName         string         `json:"tool_name"`
	Args             map[string]any `json:"args"`
	ToolCallID       string         `json:"tool_call_id"`
	EventWorkflowID  string         `json:"event_workflow_id"`
	LocalChannelName string         `json:"local_channel_name,omitempty"`
	SubAgentName     string         `json:"sub_agent_name,omitempty"`
	AgentFingerprint string         `json:"agent_fingerprint,omitempty"`
}

// AddConversationMessagesInput is the input to AddConversationMessagesActivity.
type AddConversationMessagesInput struct {
	ConversationID   string               `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message `json:"messages,omitempty"`
	AgentFingerprint string               `json:"agent_fingerprint,omitempty"`
}

// AgentWorkflow runs the agent loop: LLM → tool calls → approval/execute → feed results back to LLM → repeat.
// Stops when LLM returns no tool calls, or max iterations reached.
// When Input.EventWorkflowID is set, sends agent events and approval requests to the event workflow.
func (rt *TemporalRuntime) AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (*types.AgentResponse, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("workflow: agent run started", "scope", "workflow")
	if n := len(input.SubAgentRoutes); n > 0 {
		logger.Debug("workflow: sub-agent routes snapshot",
			"scope", "workflow",
			"routeCount", n,
			"subAgentDepth", input.SubAgentDepth)
	}
	eventWorkflowID := input.EventWorkflowID
	agentName := rt.AgentSpec.Name

	maxIter := rt.AgentExecution.Limits.MaxIterations

	var activityIDSuffix string
	err := workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		return uuid.New().String()
	}).Get(&activityIDSuffix)
	if err != nil {
		return nil, err
	}

	llmActCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentLLMActivity_" + activityIDSuffix,
		StartToCloseTimeout: agentLLMActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentLLMActivityMaxAttempts),
	})
	streamActCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentLLMStreamActivity_" + activityIDSuffix,
		StartToCloseTimeout: agentLLMActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentLLMActivityMaxAttempts),
	})

	approvalTaskTimeout := rt.AgentExecution.Limits.ApprovalTimeout
	if approvalTaskTimeout == 0 {
		approvalTaskTimeout = types.MaxApprovalTimeout
	}
	if approvalTaskTimeout > types.MaxApprovalTimeout {
		approvalTaskTimeout = types.MaxApprovalTimeout
	}

	approvalCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentToolApprovalActivity_" + activityIDSuffix,
		StartToCloseTimeout: approvalTaskTimeout,
		RetryPolicy:         retryPolicy(agentToolApprovalActivityMaxAttempts),
	})
	execCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentToolExecuteActivity_" + activityIDSuffix,
		StartToCloseTimeout: agentToolExecuteActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentToolExecuteActivityMaxAttempts),
	})
	sendEventCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "SendAgentEventUpdateActivity_" + activityIDSuffix,
		StartToCloseTimeout: sendEventActivityTaskTimeout,
		RetryPolicy:         retryPolicy(sendEventActivityMaxAttempts),
	})
	convCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "ConversationActivity_" + activityIDSuffix,
		StartToCloseTimeout: conversationActivityTaskTimeout,
		RetryPolicy:         retryPolicy(conversationActivityMaxAttempts),
	})

	emitEvent := func(ev *types.AgentEvent) {
		if ev == nil {
			return
		}
		ev.AgentName = agentName
		eventTypes := input.EventTypes
		if len(eventTypes) == 0 {
			return
		}
		emit := false
		for _, et := range eventTypes {
			if et == types.AgentEventAll {
				emit = true
				break
			}
			if et == ev.Type {
				emit = true
				break
			}
		}
		if !emit {
			return
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = workflow.Now(ctx)
		}
		upd := &AgentEventUpdate{
			AgentName:        agentName,
			LocalChannelName: input.LocalChannelName,
			Event:            ev,
		}
		// SendAgentEventUpdateActivity routes via event workflow when eventWorkflowID is set, else in-memory agentChannel
		_ = workflow.ExecuteActivity(sendEventCtx, rt.SendAgentEventUpdateActivity, eventWorkflowID, upd).Get(ctx, nil)
	}

	useStreaming := input.StreamingEnabled && rt.AgentExecution.LLM.Client.IsStreamSupported()

	messages := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: input.UserPrompt}}

	lastContent := ""
	var llmResult AgentLLMResult
	for iter := 0; iter < maxIter; iter++ {

		llmInput := AgentLLMInput{
			ConversationID:   input.ConversationID,
			Messages:         messages,
			AgentFingerprint: input.AgentFingerprint,
		}

		streamInput := AgentLLMStreamInput{
			AgentName:        agentName,
			ConversationID:   input.ConversationID,
			Messages:         messages,
			EventWorkflowID:  eventWorkflowID,
			LocalChannelName: input.LocalChannelName,
			AgentFingerprint: input.AgentFingerprint,
		}

		if useStreaming {
			err = workflow.ExecuteActivity(streamActCtx, rt.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
		} else {
			err = workflow.ExecuteActivity(llmActCtx, rt.AgentLLMActivity, llmInput).Get(llmActCtx, &llmResult)
		}
		if err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			emitEvent(&types.AgentEvent{Type: types.AgentEventError, Content: err.Error(), Timestamp: workflow.Now(ctx)})
			return nil, err
		}

		if len(llmResult.ToolCalls) == 0 {
			// Final response: accumulate assistant message for conversation
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			emitEvent(&types.AgentEvent{Type: types.AgentEventComplete, Content: llmResult.Content, Timestamp: workflow.Now(ctx)})
			lastContent = llmResult.Content
			break
		} else {
			emitEvent(&types.AgentEvent{Type: types.AgentEventContent, Content: llmResult.Content, Timestamp: workflow.Now(ctx)})
		}

		for _, tc := range llmResult.ToolCalls {
			emitEvent(&types.AgentEvent{
				Type: types.AgentEventToolCall,
				ToolCall: &types.ToolCallEvent{
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Args:       tc.Args,
					Status:     types.ToolCallStatusPending,
				},
				Timestamp: workflow.Now(ctx),
			})
		}

		if iter == maxIter-1 {
			logger.Info("workflow: max iterations reached, final LLM round without tools", "scope", "workflow", "iteration", iter)
			if useStreaming {
				streamInput.SkipTools = true
				err = workflow.ExecuteActivity(streamActCtx, rt.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
			} else {
				llmInput.SkipTools = true
				err = workflow.ExecuteActivity(llmActCtx, rt.AgentLLMActivity, llmInput).Get(llmActCtx, &llmResult)
			}
			if err != nil {
				if temporal.IsCanceledError(err) {
					return nil, err
				}
				return nil, err
			}
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			emitEvent(&types.AgentEvent{Type: types.AgentEventComplete, Content: llmResult.Content, Timestamp: workflow.Now(ctx)})
			lastContent = llmResult.Content
			break
		}

		var toolResults []interfaces.Message
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

		for _, tc := range llmResult.ToolCalls {
			approvalStatus := types.ApprovalStatusApproved
			if tc.NeedsApproval {
				logger.Info("workflow: tool requires approval", "scope", "workflow", "toolName", tc.ToolName, "argCount", len(tc.Args))
				var status types.ApprovalStatus
				approvalInput := AgentToolApprovalInput{
					ToolCallID:       tc.ToolCallID,
					ToolName:         tc.ToolName,
					Args:             tc.Args,
					EventWorkflowID:  eventWorkflowID,
					LocalChannelName: input.LocalChannelName,
					AgentFingerprint: input.AgentFingerprint,
				}
				if route, ok := input.SubAgentRoutes[tc.ToolName]; ok {
					approvalInput.SubAgentName = route.Name
				}
				if err := workflow.ExecuteActivity(approvalCtx, rt.AgentToolApprovalActivity, approvalInput).Get(approvalCtx, &status); err != nil {
					return nil, err
				}
				approvalStatus = status
			}

			var content string
			if approvalStatus == types.ApprovalStatusApproved {
				if route, ok := input.SubAgentRoutes[tc.ToolName]; ok {
					logger.Info("workflow: executing sub-agent delegation",
						"scope", "workflow",
						"tool", tc.ToolName,
						"toolCallID", tc.ToolCallID,
						"childTaskQueue", strings.TrimSpace(route.TaskQueue),
						"subAgentDepth", input.SubAgentDepth)
					content = rt.delegateToSubAgent(ctx, input, tc, route)
				} else {
					logger.Info("workflow: executing tool",
						"scope", "workflow",
						"tool", tc.ToolName,
						"toolCallID", tc.ToolCallID)
					var result string
					execInput := AgentToolExecuteInput{
						ToolName:         tc.ToolName,
						Args:             tc.Args,
						ConversationID:   input.ConversationID,
						ToolCallID:       tc.ToolCallID,
						AgentFingerprint: input.AgentFingerprint,
					}
					errExec := workflow.ExecuteActivity(execCtx, rt.AgentToolExecuteActivity, execInput).Get(execCtx, &result)
					if errExec != nil {
						content = "Tool execution failed: " + errExec.Error()
					} else {
						content = result
					}
				}
			} else {
				content = "Tool execution was rejected by the user."
			}
			emitEvent(&types.AgentEvent{
				Type: types.AgentEventToolResult,
				ToolCall: &types.ToolCallEvent{
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Args:       tc.Args,
					Result:     content,
					Status:     types.ToolCallStatusCompleted,
				},
				Timestamp: workflow.Now(ctx),
			})

			toolResults = append(toolResults, interfaces.Message{
				Role:       interfaces.MessageRoleTool,
				Content:    content,
				ToolName:   tc.ToolName,
				ToolCallID: tc.ToolCallID,
			})
		}
		messages = append(messages, toolResults...)
	}

	// Add all accumulated messages to conversation after execution completes (only when conversationID set)
	if input.ConversationID != "" {
		if len(messages) == 0 {
			logger.Debug("workflow: no conversation messages to persist", "scope", "workflow", "conversationID", input.ConversationID)
		} else {
			if err := workflow.ExecuteActivity(convCtx, rt.AddConversationMessagesActivity, AddConversationMessagesInput{
				ConversationID:   input.ConversationID,
				Messages:         messages,
				AgentFingerprint: input.AgentFingerprint,
			}).Get(convCtx, nil); err != nil {
				logger.Warn("workflow: persist conversation failed", "scope", "workflow", "conversationID", input.ConversationID, "messagesCount", len(messages), "error", err)
				return nil, err
			}
		}
	}

	// Log summary only; avoid full content to prevent leaking sensitive data
	logger.Info("workflow: agent run completed", "scope", "workflow", "contentLen", len(lastContent))
	return &types.AgentResponse{
		Content:   lastContent,
		AgentName: rt.AgentSpec.Name,
		Model:     rt.AgentExecution.LLM.Client.GetModel(),
		Metadata:  map[string]any{},
	}, nil
}

// AgentLLMStreamActivity streams LLM response tokens and emits content_delta/thinking_delta events.
// When input.ConversationID is set, fetches messages from conversation and prepends to workflow messages.
func (rt *TemporalRuntime) AgentLLMStreamActivity(ctx context.Context, input AgentLLMStreamInput) (*AgentLLMResult, error) {
	if err := rt.verifyAgentFingerprint(input.AgentFingerprint); err != nil {
		return nil, err
	}
	logger := activity.GetLogger(ctx)
	info := activity.GetInfo(ctx)
	agentWorkflowID := info.WorkflowExecution.ID
	agentName := strings.TrimSpace(input.AgentName)

	messages := input.Messages
	if input.ConversationID != "" {
		convMessages, err := rt.fetchConversationMessages(ctx, input.ConversationID)
		if err != nil {
			return nil, err
		}
		messages = append(convMessages, messages...)
	}

	logger.Debug("activity: LLM stream started", "scope", "activity", "runID", agentWorkflowID, "messageCount", len(messages))

	req, tools := rt.buildLLMRequest(messages, input.SkipTools)

	isLLMStreamSupported := rt.AgentExecution.LLM.Client.IsStreamSupported()
	if !isLLMStreamSupported {
		logger.Debug("activity: LLM stream unsupported, using generate", "scope", "activity")
		resp, err := rt.AgentExecution.LLM.Client.Generate(ctx, req)
		if err != nil {
			return nil, err
		}
		result, err := rt.llmResponseToResult(resp, tools)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	stream, err := rt.AgentExecution.LLM.Client.GenerateStream(ctx, req)
	if err != nil {
		return nil, err
	}

	// Emit deltas as they arrive. Route via event workflow when set; else in-memory agentChannel.
	emitDelta := func(ev *types.AgentEvent) {
		if ev == nil {
			return
		}
		upd := &AgentEventUpdate{
			AgentName:        agentName,
			LocalChannelName: input.LocalChannelName,
			Event:            ev,
		}
		if input.EventWorkflowID != "" {
			_, _ = rt.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
				WorkflowID:   input.EventWorkflowID,
				UpdateName:   agentEventName,
				Args:         []interface{}{upd},
				WaitForStage: client.WorkflowUpdateStageAccepted,
			})
		} else if rt.eventbus != nil {
			data, _ := json.Marshal(ev)
			_ = rt.eventbus.Publish(ctx, input.LocalChannelName, data)
		}
	}

	for stream.Next() {
		chunk := stream.Current()
		if chunk == nil {
			continue
		}
		if chunk.ContentDelta != "" {
			emitDelta(&types.AgentEvent{Type: types.AgentEventContentDelta, AgentName: agentName, Content: chunk.ContentDelta, Timestamp: time.Now()})
		}
		if chunk.ThinkingDelta != "" {
			emitDelta(&types.AgentEvent{Type: types.AgentEventThinkingDelta, AgentName: agentName, Content: chunk.ThinkingDelta, Timestamp: time.Now()})
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	resp := stream.GetResult()
	if resp == nil {
		return nil, fmt.Errorf("stream completed without result")
	}
	logger.Debug("activity: LLM stream completed", "scope", "activity", "runID", agentWorkflowID)
	result, err := rt.llmResponseToResult(resp, tools)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// buildLLMRequest builds an LLMRequest from messages and skipTools. Returns the request and tools list.
func (rt *TemporalRuntime) buildLLMRequest(messages []interfaces.Message, skipTools bool) (*interfaces.LLMRequest, []interfaces.Tool) {
	tools := rt.AgentExecution.Tools.Tools
	req := &interfaces.LLMRequest{
		SystemMessage:  rt.AgentSpec.SystemPrompt,
		ResponseFormat: rt.AgentSpec.ResponseFormat,
		Messages:       messages,
	}
	applyLLMSampling(llmSamplingToTypes(rt.AgentExecution.LLM.Sampling), req)
	if skipTools {
		req.Tools = []interfaces.ToolSpec{}
	} else {
		req.Tools = interfaces.ToolsToSpecs(tools)
	}
	return req, tools
}

// fetchConversationMessages fetches messages for the LLM: fetches from conversation when ConversationID is set,
func (rt *TemporalRuntime) fetchConversationMessages(ctx context.Context, conversationID string) ([]interfaces.Message, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("activity: loading conversation history", "scope", "activity", "conversationID", conversationID)

	if rt.AgentExecution.Session.Conversation == nil {
		return nil, fmt.Errorf("conversation is not configured")
	}

	limit := rt.AgentExecution.Session.ConversationSize
	if limit <= 0 {
		limit = 20
	}

	messages, err := rt.AgentExecution.Session.Conversation.ListMessages(ctx, conversationID, interfaces.WithLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("failed to list conversation messages: %w", err)
	}

	logger.Debug("activity: conversation history loaded", "scope", "activity", "messageCount", len(messages))
	return messages, nil
}

func (rt *TemporalRuntime) llmResponseToResult(resp *interfaces.LLMResponse, tools []interfaces.Tool) (*AgentLLMResult, error) {
	result := &AgentLLMResult{Content: resp.Content}
	for _, tc := range resp.ToolCalls {
		if tc == nil {
			continue
		}
		var tool interfaces.Tool
		for _, t := range tools {
			if t.Name() == tc.ToolName {
				tool = t
				break
			}
		}
		if tool == nil {
			return nil, fmt.Errorf("unknown tool: %s", tc.ToolName)
		}
		needsApproval := rt.requiresApproval(tool)
		result.ToolCalls = append(result.ToolCalls, ToolCallRequest{
			ToolCallID:    tc.ToolCallID,
			ToolName:      tc.ToolName,
			Args:          tc.Args,
			NeedsApproval: needsApproval,
		})
	}
	return result, nil
}

// AgentLLMActivity calls the LLM and returns content plus any tool calls.
// When input.ConversationID is set, fetches from store and adds assistant message on completion.
func (rt *TemporalRuntime) AgentLLMActivity(ctx context.Context, input AgentLLMInput) (*AgentLLMResult, error) {
	if err := rt.verifyAgentFingerprint(input.AgentFingerprint); err != nil {
		return nil, err
	}
	logger := activity.GetLogger(ctx)

	messages := input.Messages
	if input.ConversationID != "" {
		convMessages, err := rt.fetchConversationMessages(ctx, input.ConversationID)
		if err != nil {
			return nil, err
		}
		messages = append(convMessages, messages...)
	}

	logger.Debug("activity: LLM generate started", "scope", "activity", "messageCount", len(messages))
	req, tools := rt.buildLLMRequest(messages, input.SkipTools)
	resp, err := rt.AgentExecution.LLM.Client.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	logger.Debug("activity: LLM generate completed", "scope", "activity", "messageCount", len(messages))
	return rt.llmResponseToResult(resp, tools)
}

// AgentToolApprovalActivity blocks until the driver completes it via CompleteActivity.
// Sends approval request as AgentEventApproval on event channel (same channel for Run and Stream).
func (rt *TemporalRuntime) AgentToolApprovalActivity(ctx context.Context, input AgentToolApprovalInput) (types.ApprovalStatus, error) {
	if err := rt.verifyAgentFingerprint(input.AgentFingerprint); err != nil {
		return types.ApprovalStatusNone, err
	}
	logger := activity.GetLogger(ctx)
	logger.Debug("activity: tool approval started", "scope", "activity", "tool", input.ToolName, "remoteEventPipeline", input.EventWorkflowID != "")

	info := activity.GetInfo(ctx)
	taskTokenB64 := base64.StdEncoding.EncodeToString(info.TaskToken)

	kind := types.ToolApprovalKindTool
	subAgentName := input.SubAgentName
	if subAgentName != "" {
		kind = types.ToolApprovalKindDelegation
	}

	if kind == types.ToolApprovalKindDelegation {
		logger.Debug("activity: approval is sub-agent delegation",
			"scope", "activity",
			"tool", input.ToolName,
			"subAgent", subAgentName,
			"mainAgent", rt.AgentSpec.Name)
	}
	ev := &types.AgentEvent{
		Type:      types.AgentEventApproval,
		AgentName: rt.AgentSpec.Name,
		Approval: &types.ApprovalEvent{
			ToolCallID:    input.ToolCallID,
			ToolName:      input.ToolName,
			Args:          input.Args,
			ApprovalToken: taskTokenB64,
			Kind:          kind,
			SubAgentName:  subAgentName,
		},
		Timestamp: time.Now(),
	}

	// Route via event workflow when eventWorkflowID is set (TemporalRuntime.enableRemoteWorkers)
	if input.EventWorkflowID != "" {
		upd := &AgentEventUpdate{
			AgentName:        rt.AgentSpec.Name,
			LocalChannelName: input.LocalChannelName,
			Event:            ev,
		}
		_, err := rt.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   input.EventWorkflowID,
			UpdateName:   agentEventName,
			Args:         []interface{}{upd},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			return types.ApprovalStatusNone, err
		}
		logger.Debug("activity: approval sent to event pipeline", "scope", "activity", "eventPipelineID", input.EventWorkflowID, "tool", input.ToolName)
	} else {
		if rt.eventbus == nil {
			return types.ApprovalStatusNone, fmt.Errorf("agentChannel required when eventWorkflowID is empty")
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return types.ApprovalStatusNone, err
		}
		if err := rt.eventbus.Publish(ctx, input.LocalChannelName, data); err != nil {
			return types.ApprovalStatusNone, err
		}
		logger.Debug("activity: approval published to local channel", "scope", "activity", "channel", input.LocalChannelName, "tool", input.ToolName)
	}
	logger.Debug("activity: approval pending driver completion", "scope", "activity", "tool", input.ToolName)
	return types.ApprovalStatusPending, activity.ErrResultPending
}

// SendAgentEventUpdateActivity sends event: via event workflow when eventWorkflowID is set; else in-memory agentChannel.
func (rt *TemporalRuntime) SendAgentEventUpdateActivity(ctx context.Context, eventWorkflowID string, upd *AgentEventUpdate) error {
	logger := activity.GetLogger(ctx)
	logger.Debug("activity: send event update started", "scope", "activity", "eventPipelineID", eventWorkflowID)

	if upd == nil || upd.Event == nil {
		return nil
	}

	if upd.Event != nil {
		logger.Debug("activity: send event update", "scope", "activity", "eventType", string(upd.Event.Type), "agent", upd.AgentName)
	}

	// Route via event workflow when eventWorkflowID is set (TemporalRuntime.enableRemoteWorkers)
	if eventWorkflowID != "" {
		_, err := rt.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   eventWorkflowID,
			UpdateName:   agentEventName,
			Args:         []interface{}{upd},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			return err
		}
		logger.Debug("activity: event sent to pipeline", "scope", "activity", "eventPipelineID", eventWorkflowID, "agent", upd.AgentName)
	} else {
		if rt.eventbus == nil {
			return fmt.Errorf("agentChannel required when eventWorkflowID is empty")
		}
		data, err := json.Marshal(upd.Event)
		if err != nil {
			return err
		}
		if err := rt.eventbus.Publish(ctx, upd.LocalChannelName, data); err != nil {
			return err
		}
		logger.Debug("activity: event sent to local channel", "scope", "activity", "channel", upd.LocalChannelName, "agent", upd.AgentName)
	}
	logger.Debug("activity: send event update completed", "scope", "activity", "agent", upd.AgentName)
	return nil
}

// AddConversationMessagesActivity adds messages to the conversation memory.
func (rt *TemporalRuntime) AddConversationMessagesActivity(ctx context.Context, input AddConversationMessagesInput) error {
	if err := rt.verifyAgentFingerprint(input.AgentFingerprint); err != nil {
		return err
	}
	conversationID := input.ConversationID
	messages := input.Messages
	logger := activity.GetLogger(ctx)

	msgCount := len(messages)

	logger.Debug("activity: add conversation messages started", "scope", "activity", "conversationID", conversationID, "messagesCount", msgCount)

	if rt.AgentExecution.Session.Conversation == nil {
		return fmt.Errorf("conversation is not configured")
	}

	for _, msg := range messages {
		if err := rt.AgentExecution.Session.Conversation.AddMessage(ctx, conversationID, msg); err != nil {
			msgCount--
			logger.Warn("activity: add conversation message failed", "scope", "activity", "conversationID", conversationID, "error", err)
		}
	}

	logger.Debug("activity: add conversation messages completed", "scope", "activity", "conversationID", conversationID, "messagesCount", msgCount)
	return nil
}

// AgentToolExecuteActivity executes a tool by name and adds tool message to conversation when ConversationID is set.
func (rt *TemporalRuntime) AgentToolExecuteActivity(ctx context.Context, input AgentToolExecuteInput) (string, error) {
	if err := rt.verifyAgentFingerprint(input.AgentFingerprint); err != nil {
		return "", err
	}
	toolName := input.ToolName
	args := input.Args
	logger := activity.GetLogger(ctx)
	logger.Debug("activity: tool execute started", "scope", "activity", "tool", toolName, "argCount", len(args))
	tools := rt.AgentExecution.Tools.Tools
	var content string
	for _, t := range tools {
		if t.Name() == toolName {
			result, err := t.Execute(ctx, args)
			if err != nil {
				return "", err
			}
			content = fmt.Sprintf("%v", result)
			logger.Debug("activity: tool execute completed", "scope", "activity", "tool", toolName)
			return content, nil
		}
	}
	logger.Warn("activity: unknown tool", "scope", "activity", "tool", toolName)
	return "", fmt.Errorf("unknown tool: %s", toolName)
}

func (rt *TemporalRuntime) delegateToSubAgent(ctx workflow.Context, input AgentWorkflowInput, tc ToolCallRequest, route types.SubAgentRoute) string {
	logger := workflow.GetLogger(ctx)
	if strings.TrimSpace(route.TaskQueue) == "" {
		logger.Warn("workflow: sub-agent delegation skipped (empty task queue)",
			"scope", "workflow",
			"tool", tc.ToolName,
			"toolCallID", tc.ToolCallID)
		return "Sub-agent delegation failed: sub-agent task queue is not configured."
	}
	maxDepth := input.MaxSubAgentDepth
	if input.SubAgentDepth >= maxDepth {
		logger.Warn("workflow: sub-agent delegation refused (max depth)",
			"scope", "workflow",
			"subAgentDepth", input.SubAgentDepth,
			"maxDepth", maxDepth,
			"tool", tc.ToolName,
			"toolCallID", tc.ToolCallID)
		return fmt.Sprintf("Sub-agent delegation refused: maximum nesting depth (%d) reached for this agent.", maxDepth)
	}

	query := subAgentQueryFromArgs(tc.Args)
	childInput := AgentWorkflowInput{
		UserPrompt:       query,
		EventWorkflowID:  input.EventWorkflowID,
		LocalChannelName: input.LocalChannelName,
		StreamingEnabled: input.StreamingEnabled,
		ConversationID:   "",
		AgentFingerprint: route.AgentFingerprint,
		EventTypes:       input.EventTypes,
		SubAgentDepth:    input.SubAgentDepth + 1,
		SubAgentRoutes:   route.ChildRoutes,
	}

	var childSuffix string
	if err := workflow.SideEffect(ctx, func(workflow.Context) interface{} {
		return uuid.New().String()
	}).Get(&childSuffix); err != nil {
		logger.Warn("workflow: sub-agent child run id failed", "scope", "workflow", "error", err)
		return "Sub-agent workflow failed: " + err.Error()
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

	var childResp *types.AgentResponse
	if err := workflow.ExecuteChildWorkflow(childCtx, rt.AgentWorkflow, childInput).Get(childCtx, &childResp); err != nil {
		logger.Warn("workflow: sub-agent child run failed",
			"scope", "workflow",
			"childWorkflowID", childWfID,
			"tool", tc.ToolName,
			"error", err)
		return "Sub-agent workflow failed: " + err.Error()
	}

	logger.Debug("workflow: sub-agent child run completed",
		"scope", "workflow",
		"childWorkflowID", childWfID,
		"tool", tc.ToolName,
		"responseContentLen", len(childResp.Content))

	return childResp.Content
}

func (rt *TemporalRuntime) requiresApproval(t interfaces.Tool) bool {
	if rt.AgentExecution.Tools.ApprovalPolicy == nil {
		// No policy: honor tool's ApprovalRequired
		if ar, ok := t.(interfaces.ToolApproval); ok && ar.ApprovalRequired() {
			return true
		}
		return false
	}
	// Policy set: policy decides (can override tool default)
	return rt.AgentExecution.Tools.ApprovalPolicy.RequiresApproval(t)
}

func subAgentQueryFromArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	q, _ := args[types.SubAgentToolParamQuery].(string)
	return q
}

// subAgentChildWorkflowTimeout caps how long the main agent waits on a delegated sub-agent run.
// Uses the main agent worker's agent timeout (same package as delegateToSubAgent); sub-agent workers may define
// their own limits separately, but this bounds the child execution from the main agent's perspective.
func (rt *TemporalRuntime) subAgentChildWorkflowTimeout() time.Duration {
	return rt.AgentExecution.Limits.Timeout
}

func llmSamplingToTypes(s *sdkruntime.LLMSampling) *types.LLMSampling {
	if s == nil {
		return nil
	}
	out := &types.LLMSampling{
		Temperature: s.Temperature,
		MaxTokens:   s.MaxTokens,
		TopP:        s.TopP,
		TopK:        s.TopK,
	}
	if s.Reasoning != nil {
		r := *s.Reasoning
		out.Reasoning = &r
	}
	return out
}

func retryPolicy(maxAttempts int32) *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
		MaximumInterval:    10 * time.Minute,
		MaximumAttempts:    maxAttempts,
	}
}

func applyLLMSampling(sampling *types.LLMSampling, req *interfaces.LLMRequest) {
	if sampling == nil {
		return
	}
	s := sampling
	if s.Temperature != nil {
		req.Temperature = s.Temperature
	}
	if s.MaxTokens > 0 {
		req.MaxTokens = s.MaxTokens
	}
	if s.TopP != nil {
		req.TopP = s.TopP
	}
	if s.TopK != nil {
		req.TopK = s.TopK
	}
	if s.Reasoning != nil {
		r := *s.Reasoning
		req.Reasoning = &r
	}
}
