package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
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

	defaultMaxIterations int = 5
)

// SubAgentRoute tells the workflow how to delegate to a sub-agent: child AgentWorkflow on TaskQueue,
// with nested routes for that sub-agent's own sub-agents (frozen at parent run start).
type SubAgentRoute struct {
	TaskQueue   string                   `json:"task_queue"`
	ChildRoutes map[string]SubAgentRoute `json:"child_routes,omitempty"`
}

// AgentWorkflowInput is the input to AgentWorkflow. EventWorkflowID is set when streaming or approval is used.
// StreamingEnabled enables partial content streaming (from WithStream).
// ConversationID is set when conversation is used; workflow fetches messages and writes assistant/tool via activities.
// SubAgentDepth is 0 for a top-level user run; each child workflow increments it (runtime cap vs maxSubAgentDepth).
// SubAgentRoutes maps sub-agent tool name -> route; built from WithSubAgents when the run starts.
// LocalChannelName is the in-process pub/sub channel name used for in-memory event fan-in across the
// delegation tree. Set once at the top level (agent_event_<main-workflow-id>) and propagated unchanged
// to all sub-agents. Contrast with EventWorkflowID which is used for out-of-process (remote) routing.
// EventTypes is set by the SDK; a single "*" element means emit all event kinds (used for RunStream).
type AgentWorkflowInput struct {
	UserPrompt       string                   `json:"user_prompt,omitempty"`
	EventWorkflowID  string                   `json:"event_workflow_id,omitempty"`
	LocalChannelName string                   `json:"local_channel_name,omitempty"`
	StreamingEnabled bool                     `json:"streaming_enabled,omitempty"`
	ConversationID   string                   `json:"conversation_id,omitempty"`
	EventTypes       []AgentEventType         `json:"event_types,omitempty"`
	SubAgentDepth    int                      `json:"sub_agent_depth,omitempty"`
	SubAgentRoutes   map[string]SubAgentRoute `json:"sub_agent_routes,omitempty"`
}

// AgentLLMInput is the input to AgentLLMActivity. When ConversationID is set, activity fetches messages from store.
// UserPrompt is passed directly; no message construction in workflow. Messages used only for non-conversation multi-turn.
type AgentLLMInput struct {
	ConversationID string               `json:"conversation_id,omitempty"`
	Messages       []interfaces.Message `json:"messages,omitempty"`
	SkipTools      bool                 `json:"skip_tools,omitempty"`
}

// AgentLLMStreamInput is the input to AgentLLMStreamActivity.
type AgentLLMStreamInput struct {
	ConversationID   string               `json:"conversation_id,omitempty"`
	Messages         []interfaces.Message `json:"messages,omitempty"`
	EventWorkflowID  string               `json:"event_workflow_id,omitempty"`
	LocalChannelName string               `json:"local_channel_name,omitempty"`
	SkipTools        bool                 `json:"skip_tools,omitempty"`
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
	ToolName       string               `json:"tool_name"`
	Args           map[string]any       `json:"args"`
	ConversationID string               `json:"conversation_id,omitempty"`
	Messages       []interfaces.Message `json:"messages,omitempty"`
	ToolCallID     string               `json:"tool_call_id,omitempty"`
}

type AgentToolApprovalInput struct {
	ToolName         string         `json:"tool_name"`
	Args             map[string]any `json:"args"`
	ToolCallID       string         `json:"tool_call_id"`
	EventWorkflowID  string         `json:"event_workflow_id"`
	LocalChannelName string         `json:"local_channel_name,omitempty"`
}

// AgentWorkflow runs the agent loop: LLM → tool calls → approval/execute → feed results back to LLM → repeat.
// Stops when LLM returns no tool calls, or max iterations reached.
// When Input.EventWorkflowID is set, sends agent events and approval requests to the event workflow.
func (aw *AgentWorker) AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (*AgentResponse, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("agent workflow started")
	if n := len(input.SubAgentRoutes); n > 0 {
		logger.Debug("agent workflow sub-agent routes snapshot",
			zap.Int("routeCount", n),
			zap.Int("subAgentDepth", input.SubAgentDepth))
	}
	eventWorkflowID := input.EventWorkflowID
	agentName := strings.TrimSpace(aw.config.Name)

	maxIter := aw.config.maxIterations
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}

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
	approvalTaskTimeout := aw.config.approvalTimeout
	if approvalTaskTimeout == 0 {
		approvalTaskTimeout = maxApprovalTimeout
	}
	if approvalTaskTimeout > maxApprovalTimeout {
		approvalTaskTimeout = maxApprovalTimeout
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

	emitEvent := func(ev *AgentEvent) {
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
			if et == agentEventAll {
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
		_ = workflow.ExecuteActivity(sendEventCtx, aw.SendAgentEventUpdateActivity, eventWorkflowID, upd).Get(ctx, nil)
	}

	useStreaming := input.StreamingEnabled && aw.config.LLMClient.IsStreamSupported()

	messages := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: input.UserPrompt}}

	lastContent := ""
	var llmResult AgentLLMResult
	for iter := 0; iter < maxIter; iter++ {

		llmInput := AgentLLMInput{
			ConversationID: input.ConversationID,
			Messages:       messages,
		}

		streamInput := AgentLLMStreamInput{
			ConversationID:   input.ConversationID,
			Messages:         messages,
			EventWorkflowID:  eventWorkflowID,
			LocalChannelName: input.LocalChannelName,
		}

		if useStreaming {
			err = workflow.ExecuteActivity(streamActCtx, aw.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
		} else {
			err = workflow.ExecuteActivity(llmActCtx, aw.AgentLLMActivity, llmInput).Get(llmActCtx, &llmResult)
		}
		if err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			emitEvent(&AgentEvent{Type: AgentEventError, Content: err.Error(), Timestamp: workflow.Now(ctx)})
			return nil, err
		}

		if len(llmResult.ToolCalls) == 0 {
			// Final response: accumulate assistant message for conversation
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			emitEvent(&AgentEvent{Type: AgentEventComplete, Content: llmResult.Content, Timestamp: workflow.Now(ctx)})
			lastContent = llmResult.Content
			break
		} else {
			emitEvent(&AgentEvent{Type: AgentEventContent, Content: llmResult.Content, Timestamp: workflow.Now(ctx)})
		}

		for _, tc := range llmResult.ToolCalls {
			emitEvent(&AgentEvent{
				Type: AgentEventToolCall,
				ToolCall: &ToolCallEvent{
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Args:       tc.Args,
					Status:     ToolCallStatusPending,
				},
				Timestamp: workflow.Now(ctx),
			})
		}

		if iter == maxIter-1 {
			logger.Info("max iterations reached, calling LLM once more without tools for final response", zap.Int("iteration", iter))
			if useStreaming {
				streamInput.SkipTools = true
				err = workflow.ExecuteActivity(streamActCtx, aw.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
			} else {
				llmInput.SkipTools = true
				err = workflow.ExecuteActivity(llmActCtx, aw.AgentLLMActivity, llmInput).Get(llmActCtx, &llmResult)
			}
			if err != nil {
				if temporal.IsCanceledError(err) {
					return nil, err
				}
				return nil, err
			}
			messages = append(messages, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: llmResult.Content})
			emitEvent(&AgentEvent{Type: AgentEventComplete, Content: llmResult.Content, Timestamp: workflow.Now(ctx)})
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
			approvalStatus := ApprovalStatusApproved
			if tc.NeedsApproval {
				logger.Info("approval required for tool", zap.String("toolName", tc.ToolName), zap.Int("argCount", len(tc.Args)))
				var status ApprovalStatus
				approvalInput := AgentToolApprovalInput{
					ToolCallID:       tc.ToolCallID,
					ToolName:         tc.ToolName,
					Args:             tc.Args,
					EventWorkflowID:  eventWorkflowID,
					LocalChannelName: input.LocalChannelName,
				}
				if err := workflow.ExecuteActivity(approvalCtx, aw.AgentToolApprovalActivity, approvalInput).Get(approvalCtx, &status); err != nil {
					return nil, err
				}
				approvalStatus = status
			}

			var content string
			if approvalStatus == ApprovalStatusApproved {
				if route, ok := input.SubAgentRoutes[tc.ToolName]; ok {
					logger.Info("executing tool call",
						zap.String("executionKind", "sub_agent"),
						zap.String("tool", tc.ToolName),
						zap.String("toolCallID", tc.ToolCallID),
						zap.String("childTaskQueue", strings.TrimSpace(route.TaskQueue)),
						zap.Int("subAgentDepth", input.SubAgentDepth))
					content = aw.delegateToSubAgent(ctx, input, tc, route)
				} else {
					logger.Info("executing tool call",
						zap.String("executionKind", "tool"),
						zap.String("tool", tc.ToolName),
						zap.String("toolCallID", tc.ToolCallID))
					var result string
					execInput := AgentToolExecuteInput{
						ToolName:       tc.ToolName,
						Args:           tc.Args,
						ConversationID: input.ConversationID,
						ToolCallID:     tc.ToolCallID,
					}
					errExec := workflow.ExecuteActivity(execCtx, aw.AgentToolExecuteActivity, execInput).Get(execCtx, &result)
					if errExec != nil {
						content = "Tool execution failed: " + errExec.Error()
					} else {
						content = result
					}
				}
			} else {
				content = "Tool execution was rejected by the user."
			}
			emitEvent(&AgentEvent{
				Type: AgentEventToolResult,
				ToolCall: &ToolCallEvent{
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Args:       tc.Args,
					Result:     content,
					Status:     ToolCallStatusCompleted,
				},
				Timestamp: workflow.Now(ctx),
			})

			toolResults = append(toolResults, interfaces.Message{
				Role:       interfaces.MessageRoleTool,
				Content:    content,
				ToolCallID: tc.ToolCallID,
			})
		}
		messages = append(messages, toolResults...)
	}

	// Add all accumulated messages to conversation after execution completes (only when conversationID set)
	if input.ConversationID != "" {
		if len(messages) == 0 {
			logger.Info("no messages to add to conversation", zap.String("conversationID", input.ConversationID))
		} else {
			if err := workflow.ExecuteActivity(convCtx, aw.AddConversationMessagesActivity, input.ConversationID, messages).Get(convCtx, nil); err != nil {
				logger.Warn("failed to add conversation messages", zap.String("conversationID", input.ConversationID), zap.Any("messagesCount", len(messages)), zap.Error(err))
				return nil, err
			}
		}
	}

	// Log summary only; avoid full content to prevent leaking sensitive data
	logger.Info("agent workflow completed", zap.Int("contentLen", len(lastContent)))
	return &AgentResponse{
		Content:   lastContent,
		AgentName: aw.config.Name,
		Model:     aw.config.LLMClient.GetModel(),
		Metadata:  map[string]any{},
	}, nil
}

// AgentLLMStreamActivity streams LLM response tokens and emits content_delta/thinking_delta events.
// When input.ConversationID is set, fetches messages from conversation and prepends to workflow messages.
func (aw *AgentWorker) AgentLLMStreamActivity(ctx context.Context, input AgentLLMStreamInput) (*AgentLLMResult, error) {
	logger := activity.GetLogger(ctx)
	info := activity.GetInfo(ctx)
	agentWorkflowID := info.WorkflowExecution.ID
	agentName := strings.TrimSpace(aw.config.Name)

	messages := input.Messages
	if input.ConversationID != "" {
		convMessages, err := aw.fetchConversationMessages(ctx, input.ConversationID)
		if err != nil {
			return nil, err
		}
		messages = append(convMessages, messages...)
	}

	logger.Debug("agent LLM stream activity started", zap.String("agentWorkflowID", agentWorkflowID), zap.Int("messageCount", len(messages)))

	req, tools := aw.buildLLMRequest(messages, input.SkipTools)

	isLLMStreamSupported := aw.config.LLMClient.IsStreamSupported()
	if !isLLMStreamSupported {
		logger.Debug("llm does not support streaming, falling back to generate")
		resp, err := aw.config.LLMClient.Generate(ctx, req)
		if err != nil {
			return nil, err
		}
		result, err := aw.llmResponseToResult(resp, tools)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	stream, err := aw.config.LLMClient.GenerateStream(ctx, req)
	if err != nil {
		return nil, err
	}

	// Emit deltas as they arrive. Route via event workflow when set; else in-memory agentChannel.
	emitDelta := func(ev *AgentEvent) {
		if ev == nil {
			return
		}
		upd := &AgentEventUpdate{
			AgentName:        agentName,
			LocalChannelName: input.LocalChannelName,
			Event:            ev,
		}
		if input.EventWorkflowID != "" {
			_, _ = aw.config.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
				WorkflowID:   input.EventWorkflowID,
				UpdateName:   agentEventName,
				Args:         []interface{}{upd},
				WaitForStage: client.WorkflowUpdateStageAccepted,
			})
		} else if aw.agentChannel != nil {
			data, _ := json.Marshal(ev)
			_ = aw.agentChannel.Publish(ctx, input.LocalChannelName, data)
		}
	}

	for stream.Next() {
		chunk := stream.Current()
		if chunk == nil {
			continue
		}
		if chunk.ContentDelta != "" {
			emitDelta(&AgentEvent{Type: AgentEventContentDelta, AgentName: agentName, Content: chunk.ContentDelta, Timestamp: time.Now()})
		}
		if chunk.ThinkingDelta != "" {
			emitDelta(&AgentEvent{Type: AgentEventThinkingDelta, AgentName: agentName, Content: chunk.ThinkingDelta, Timestamp: time.Now()})
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	resp := stream.GetResult()
	if resp == nil {
		return nil, fmt.Errorf("stream completed without result")
	}
	logger.Debug("agent LLM stream activity completed", zap.String("agentWorkflowID", agentWorkflowID))
	result, err := aw.llmResponseToResult(resp, tools)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// buildLLMRequest builds an LLMRequest from messages and skipTools. Returns the request and tools list.
func (aw *AgentWorker) buildLLMRequest(messages []interfaces.Message, skipTools bool) (*interfaces.LLMRequest, []interfaces.Tool) {
	tools := aw.config.toolsList()
	req := &interfaces.LLMRequest{
		SystemMessage:  aw.config.SystemPrompt,
		ResponseFormat: aw.config.responseFormatForLLM(),
		Messages:       messages,
	}
	aw.config.applySamplingToRequest(req)
	if skipTools {
		req.Tools = []interfaces.ToolSpec{}
	} else {
		req.Tools = interfaces.ToolsToSpecs(tools)
	}
	return req, tools
}

// fetchConversationMessages fetches messages for the LLM: fetches from conversation when ConversationID is set,
func (aw *AgentWorker) fetchConversationMessages(ctx context.Context, conversationID string) ([]interfaces.Message, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("fetching conversation messages", zap.String("conversationID", conversationID))

	if aw.config == nil || aw.config.conversation == nil {
		return nil, fmt.Errorf("conversation is not configured")
	}

	limit := aw.config.conversationSize
	if limit <= 0 {
		limit = 20
	}

	messages, err := aw.config.conversation.ListMessages(ctx, conversationID, interfaces.WithLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("failed to list conversation messages: %w", err)
	}

	logger.Debug("conversation messages fetched", zap.Int("messageCount", len(messages)))
	return messages, nil
}

func (aw *AgentWorker) llmResponseToResult(resp *interfaces.LLMResponse, tools []interfaces.Tool) (*AgentLLMResult, error) {
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
		needsApproval := aw.config.requiresApproval(tool)
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
func (aw *AgentWorker) AgentLLMActivity(ctx context.Context, input AgentLLMInput) (*AgentLLMResult, error) {
	logger := activity.GetLogger(ctx)

	messages := input.Messages
	if input.ConversationID != "" {
		convMessages, err := aw.fetchConversationMessages(ctx, input.ConversationID)
		if err != nil {
			return nil, err
		}
		messages = append(convMessages, messages...)
	}

	logger.Debug("agent LLM activity started", zap.Int("messageCount", len(messages)))
	req, tools := aw.buildLLMRequest(messages, input.SkipTools)
	resp, err := aw.config.LLMClient.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	logger.Debug("agent LLM activity completed", zap.Int("messageCount", len(messages)))
	return aw.llmResponseToResult(resp, tools)
}

func toolApprovalMetadata(aw *AgentWorker, toolName string) (kind ToolApprovalKind, agentName, delegateToName string) {
	if aw == nil || aw.config == nil {
		return ToolApprovalKindTool, "", ""
	}
	agentName = strings.TrimSpace(aw.config.Name)
	kind = ToolApprovalKindTool
	for _, t := range aw.config.toolsList() {
		if t.Name() != toolName {
			continue
		}
		if at, ok := t.(AgentTool); ok {
			kind = ToolApprovalKindDelegation
			delegateToName = subAgentLabel(at.SubAgent())
		}
		break
	}
	return kind, agentName, delegateToName
}

// AgentToolApprovalActivity blocks until the driver completes it via CompleteActivity.
// Sends approval request as AgentEventApproval on event channel (same channel for Run and RunStream).
func (aw *AgentWorker) AgentToolApprovalActivity(ctx context.Context, input AgentToolApprovalInput) (ApprovalStatus, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("agent tool approval activity started", zap.String("tool", input.ToolName), zap.Bool("viaEventWorkflow", input.EventWorkflowID != ""))

	info := activity.GetInfo(ctx)
	taskTokenB64 := base64.StdEncoding.EncodeToString(info.TaskToken)

	kind, agentName, delegateToName := toolApprovalMetadata(aw, input.ToolName)
	if kind == ToolApprovalKindDelegation {
		logger.Debug("tool approval targets sub-agent delegation",
			zap.String("tool", input.ToolName),
			zap.String("delegateTo", delegateToName),
			zap.String("mainAgent", agentName))
	}
	ev := &AgentEvent{
		Type:      AgentEventApproval,
		AgentName: agentName,
		Approval: &ApprovalEvent{
			ToolCallID:     input.ToolCallID,
			ToolName:       input.ToolName,
			Args:           input.Args,
			ApprovalToken:  taskTokenB64,
			Kind:           kind,
			DelegateToName: delegateToName,
		},
		Timestamp: time.Now(),
	}

	// Route via event workflow when eventWorkflowID is set (Agent sets this when enableRemoteWorkers is true)
	if input.EventWorkflowID != "" {
		upd := &AgentEventUpdate{
			AgentName:        agentName,
			LocalChannelName: input.LocalChannelName,
			Event:            ev,
		}
		_, err := aw.config.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   input.EventWorkflowID,
			UpdateName:   agentEventName,
			Args:         []interface{}{upd},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			return ApprovalStatusNone, err
		}
		logger.Debug("approval request sent to event workflow", zap.String("eventWorkflowID", input.EventWorkflowID), zap.String("tool", input.ToolName))
	} else {
		if aw.agentChannel == nil {
			return ApprovalStatusNone, fmt.Errorf("agentChannel required when eventWorkflowID is empty")
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return ApprovalStatusNone, err
		}
		if err := aw.agentChannel.Publish(ctx, input.LocalChannelName, data); err != nil {
			return ApprovalStatusNone, err
		}
		logger.Debug("approval event published to event channel", zap.String("channel", input.LocalChannelName), zap.String("tool", input.ToolName))
	}
	logger.Debug("approval request sent, waiting for completion", zap.String("tool", input.ToolName))
	return ApprovalStatusPending, activity.ErrResultPending
}

// SendAgentEventUpdateActivity sends event: via event workflow when eventWorkflowID is set; else in-memory agentChannel.
func (aw *AgentWorker) SendAgentEventUpdateActivity(ctx context.Context, eventWorkflowID string, upd *AgentEventUpdate) error {
	logger := activity.GetLogger(ctx)
	logger.Debug("send agent event update activity started", zap.String("eventWorkflowID", eventWorkflowID), zap.Any("upd", upd))

	if upd == nil || upd.Event == nil {
		return nil
	}

	if upd.Event != nil {
		logger.Debug("send agent event update activity", zap.String("eventType", string(upd.Event.Type)), zap.String("agent", upd.AgentName))
	}

	// Route via event workflow when eventWorkflowID is set (Agent sets this when enableRemoteWorkers is true)
	if eventWorkflowID != "" {
		_, err := aw.config.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   eventWorkflowID,
			UpdateName:   agentEventName,
			Args:         []interface{}{upd},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			return err
		}
		logger.Debug("agent event sent to event workflow", zap.String("eventWorkflowID", eventWorkflowID), zap.String("agent", upd.AgentName))
	} else {
		if aw.agentChannel == nil {
			return fmt.Errorf("agentChannel required when eventWorkflowID is empty")
		}
		data, err := json.Marshal(upd.Event)
		if err != nil {
			return err
		}
		if err := aw.agentChannel.Publish(ctx, upd.LocalChannelName, data); err != nil {
			return err
		}
		logger.Debug("agent event sent to channel", zap.String("channel", upd.LocalChannelName), zap.String("agent", upd.AgentName))
	}
	logger.Debug("agent event update activity completed", zap.String("agent", upd.AgentName))
	return nil
}

// AddConversationMessagesActivity adds messages to the conversation memory.
func (aw *AgentWorker) AddConversationMessagesActivity(ctx context.Context, conversationID string, messages []interfaces.Message) error {
	logger := activity.GetLogger(ctx)

	msgCount := len(messages)

	logger.Debug("add conversation messages activity started", zap.String("conversationID", conversationID), zap.Any("messagesCount", msgCount))

	if aw.config == nil || aw.config.conversation == nil {
		return fmt.Errorf("conversation is not configured")
	}

	for _, msg := range messages {
		if err := aw.config.conversation.AddMessage(ctx, conversationID, msg); err != nil {
			msgCount--
			logger.Warn("failed to add conversation message", zap.String("conversationID", conversationID), zap.Any("msg", msg), zap.Error(err))
		}
	}

	logger.Debug("add conversation messages activity completed", zap.String("conversationID", conversationID), zap.Int("messagesCount", msgCount))
	return nil
}

// AgentToolExecuteActivity executes a tool by name and adds tool message to conversation when ConversationID is set.
func (aw *AgentWorker) AgentToolExecuteActivity(ctx context.Context, input AgentToolExecuteInput) (string, error) {
	toolName := input.ToolName
	args := input.Args
	logger := activity.GetLogger(ctx)
	logger.Debug("agent tool execute activity started", zap.String("tool", toolName), zap.Int("argCount", len(args)))
	tools := aw.config.toolsList()
	var content string
	for _, t := range tools {
		if t.Name() == toolName {
			result, err := t.Execute(ctx, args)
			if err != nil {
				return "", err
			}
			content = fmt.Sprintf("%v", result)
			logger.Debug("agent tool execute activity completed", zap.String("tool", toolName))
			return content, nil
		}
	}
	logger.Warn("unknown tool", zap.String("tool", toolName))
	return "", fmt.Errorf("unknown tool: %s", toolName)
}

func (aw *AgentWorker) delegateToSubAgent(ctx workflow.Context, input AgentWorkflowInput, tc ToolCallRequest, route SubAgentRoute) string {
	logger := workflow.GetLogger(ctx)
	if strings.TrimSpace(route.TaskQueue) == "" {
		logger.Warn("sub-agent delegation skipped: empty child task queue",
			zap.String("tool", tc.ToolName),
			zap.String("toolCallID", tc.ToolCallID))
		return "Sub-agent delegation failed: sub-agent task queue is not configured."
	}
	maxDepth := aw.config.maxSubAgentDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxSubAgentDepth
	}
	if input.SubAgentDepth >= maxDepth {
		logger.Warn("sub-agent delegation refused: max nesting depth",
			zap.Int("subAgentDepth", input.SubAgentDepth),
			zap.Int("maxDepth", maxDepth),
			zap.String("tool", tc.ToolName),
			zap.String("toolCallID", tc.ToolCallID))
		return fmt.Sprintf("Sub-agent delegation refused: maximum nesting depth (%d) reached for this agent.", maxDepth)
	}

	query := subAgentQueryFromArgs(tc.Args)
	childInput := AgentWorkflowInput{
		UserPrompt:       query,
		EventWorkflowID:  input.EventWorkflowID,
		LocalChannelName: input.LocalChannelName,
		StreamingEnabled: input.StreamingEnabled,
		ConversationID:   "",
		EventTypes:       input.EventTypes,
		SubAgentDepth:    input.SubAgentDepth + 1,
		SubAgentRoutes:   route.ChildRoutes,
	}

	var childSuffix string
	if err := workflow.SideEffect(ctx, func(workflow.Context) interface{} {
		return uuid.New().String()
	}).Get(&childSuffix); err != nil {
		logger.Warn("sub-agent child workflow id generation failed", zap.Error(err))
		return "Sub-agent workflow failed: " + err.Error()
	}

	parentID := workflow.GetInfo(ctx).WorkflowExecution.ID
	childWfID := fmt.Sprintf("%s-sub-%s-%s", parentID, tc.ToolCallID, childSuffix)
	childTO := subAgentChildWorkflowTimeout(aw)

	logger.Debug("starting sub-agent child workflow",
		zap.String("childWorkflowID", childWfID),
		zap.String("childTaskQueue", strings.TrimSpace(route.TaskQueue)),
		zap.String("tool", tc.ToolName),
		zap.String("toolCallID", tc.ToolCallID),
		zap.Int("parentSubAgentDepth", input.SubAgentDepth),
		zap.Int("childSubAgentDepth", childInput.SubAgentDepth),
		zap.Int("nestedChildRouteCount", len(route.ChildRoutes)),
		zap.Duration("workflowExecutionTimeout", childTO),
		zap.Int("delegatedQueryLen", len(query)))

	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:               childWfID,
		TaskQueue:                route.TaskQueue,
		WorkflowExecutionTimeout: childTO,
		ParentClosePolicy:        enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
		WaitForCancellation:      true,
	})

	var childResp AgentResponse
	if err := workflow.ExecuteChildWorkflow(childCtx, aw.AgentWorkflow, childInput).Get(childCtx, &childResp); err != nil {
		logger.Warn("sub-agent child workflow failed",
			zap.String("childWorkflowID", childWfID),
			zap.String("tool", tc.ToolName),
			zap.Error(err))
		return "Sub-agent workflow failed: " + err.Error()
	}

	logger.Debug("sub-agent child workflow completed",
		zap.String("childWorkflowID", childWfID),
		zap.String("tool", tc.ToolName),
		zap.Int("responseContentLen", len(childResp.Content)))

	return childResp.Content
}

func subAgentQueryFromArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	q, _ := args[subAgentToolParamQuery].(string)
	return q
}

// subAgentChildWorkflowTimeout caps how long the main agent waits on a delegated sub-agent run.
// Uses the main agent worker's agent timeout (same package as delegateToSubAgent); sub-agent workers may define
// their own limits separately, but this bounds the child execution from the main agent's perspective.
func subAgentChildWorkflowTimeout(aw *AgentWorker) time.Duration {
	if aw != nil && aw.config != nil && aw.config.timeout > 0 {
		return aw.config.timeout
	}
	return defaultTimeout
}

func retryPolicy(maxAttempts int32) *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
		MaximumInterval:    10 * time.Minute,
		MaximumAttempts:    maxAttempts,
	}
}
