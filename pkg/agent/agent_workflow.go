package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

var (
	agentLLMActivityTaskTimeout time.Duration = 30 * time.Minute
	agentLLMActivityMaxAttempts int32         = 3

	agentToolApprovalActivityTaskTimeout time.Duration = time.Minute * 60 * 24 * 31
	agentToolApprovalActivityMaxAttempts int32         = 3

	agentToolExecuteActivityTaskTimeout time.Duration = 30 * time.Minute
	agentToolExecuteActivityMaxAttempts int32         = 3

	defaultMaxIterations int = 5
)

// AgentWorkflowInput is the input to AgentWorkflow. EventWorkflowID is set when streaming or approval is used.
// StreamingEnabled enables partial content streaming (from WithStream).
type AgentWorkflowInput struct {
	Input            string
	EventWorkflowID  string
	StreamingEnabled bool
}

// AgentLLMStreamInput is the input to AgentLLMStreamActivity.
type AgentLLMStreamInput struct {
	Messages        []interfaces.Message
	EventWorkflowID string
	AgentWorkflowID string
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

// AgentWorkflow runs the agent loop: LLM → tool calls → approval/execute → feed results back to LLM → repeat.
// Stops when LLM returns no tool calls, or max iterations reached.
// When Input.EventWorkflowID is set, sends agent events and approval requests to the event workflow.
func (aw *AgentWorker) AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (*AgentResponse, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("agent workflow started")
	eventWorkflowID := input.EventWorkflowID
	agentWorkflowID := workflow.GetInfo(ctx).WorkflowExecution.ID

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

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentLLMActivity_" + activityIDSuffix,
		StartToCloseTimeout: agentLLMActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentLLMActivityMaxAttempts),
	})
	approvalCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentToolApprovalActivity_" + activityIDSuffix,
		StartToCloseTimeout: agentToolApprovalActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentToolApprovalActivityMaxAttempts),
	})
	execCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentToolExecuteActivity_" + activityIDSuffix,
		StartToCloseTimeout: agentToolExecuteActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentToolExecuteActivityMaxAttempts),
	})
	sendEventCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "SendAgentEventUpdateActivity_" + activityIDSuffix,
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         retryPolicy(3),
	})

	messages := []interfaces.Message{{Role: "user", Content: input.Input}}

	emitEvent := func(ev *AgentEvent) {
		if ev == nil {
			return
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = workflow.Now(ctx)
		}
		upd := &AgentEventUpdate{AgentWorkflowID: agentWorkflowID, Event: ev}
		// SendAgentEventUpdateActivity routes via event workflow when eventWorkflowID is set, else in-memory agentChannel
		_ = workflow.ExecuteActivity(sendEventCtx, aw.SendAgentEventUpdateActivity, eventWorkflowID, upd).Get(ctx, nil)
	}

	isLLMStreamSupported := aw.config.LLMClient.IsStreamSupported()

	useStreaming := input.StreamingEnabled && isLLMStreamSupported
	streamActCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:          "AgentLLMStreamActivity_" + activityIDSuffix,
		StartToCloseTimeout: agentLLMActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentLLMActivityMaxAttempts),
	})

	lastContent := ""
	for iter := 0; iter < maxIter; iter++ {
		var llmResult AgentLLMResult
		if useStreaming {
			streamInput := AgentLLMStreamInput{
				Messages:        messages,
				EventWorkflowID: eventWorkflowID,
				AgentWorkflowID: agentWorkflowID,
			}
			err = workflow.ExecuteActivity(streamActCtx, aw.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
		} else {
			err = workflow.ExecuteActivity(actCtx, aw.AgentLLMActivity, messages).Get(actCtx, &llmResult)
		}
		if err != nil {
			if temporal.IsCanceledError(err) {
				return nil, err
			}
			emitEvent(&AgentEvent{Type: AgentEventError, Content: err.Error(), Timestamp: workflow.Now(ctx)})
			return nil, err
		}

		lastContent = llmResult.Content
		emitEvent(&AgentEvent{Type: AgentEventContent, Content: llmResult.Content, Timestamp: workflow.Now(ctx)})
		if len(llmResult.ToolCalls) == 0 {
			emitEvent(&AgentEvent{Type: AgentEventComplete, Content: lastContent, Timestamp: workflow.Now(ctx)})
			break
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
			logger.Info("max iterations reached, calling LLM once more for final response", zap.Int("iteration", iter))
			if useStreaming {
				streamInput := AgentLLMStreamInput{Messages: messages, EventWorkflowID: eventWorkflowID, AgentWorkflowID: agentWorkflowID}
				err = workflow.ExecuteActivity(streamActCtx, aw.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
			} else {
				err = workflow.ExecuteActivity(actCtx, aw.AgentLLMActivity, messages).Get(actCtx, &llmResult)
			}
			if err != nil {
				if temporal.IsCanceledError(err) {
					return nil, err
				}
				return nil, err
			}
			lastContent = llmResult.Content
			emitEvent(&AgentEvent{Type: AgentEventComplete, Content: lastContent, Timestamp: workflow.Now(ctx)})
			break
		}

		assistantMsg := interfaces.Message{
			Role:      "assistant",
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

		var toolResults []interfaces.Message
		for _, tc := range llmResult.ToolCalls {
			approvalStatus := ApprovalStatusApproved
			if tc.NeedsApproval {
				logger.Info("approval required for tool", zap.String("toolName", tc.ToolName), zap.Int("argCount", len(tc.Args)))
				var status ApprovalStatus
				if err := workflow.ExecuteActivity(approvalCtx, aw.AgentToolApprovalActivity, eventWorkflowID, tc.ToolName, tc.Args).Get(approvalCtx, &status); err != nil {
					return nil, err
				}
				approvalStatus = status
			}

			var content string
			if approvalStatus == ApprovalStatusApproved {
				var result string
				if err := workflow.ExecuteActivity(execCtx, aw.AgentToolExecuteActivity, tc.ToolName, tc.Args).Get(execCtx, &result); err != nil {
					content = "Tool execution failed: " + err.Error()
				} else {
					content = result
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
				Role:       "tool",
				Content:    content,
				ToolCallID: tc.ToolCallID,
			})
		}
		messages = append(messages, toolResults...)
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
// Falls back to Generate when the client does not support streaming.
func (aw *AgentWorker) AgentLLMStreamActivity(ctx context.Context, input AgentLLMStreamInput) (*AgentLLMResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("agent LLM stream activity started", zap.String("agentWorkflowID", input.AgentWorkflowID), zap.Int("messageCount", len(input.Messages)))
	tools := aw.config.toolsList()
	req := &interfaces.LLMRequest{
		SystemMessage: aw.config.SystemPrompt,
		ResponseFormat: &interfaces.ResponseFormat{
			Type: interfaces.ResponseFormatJSON,
			Name: "AgentResponse",
			Schema: interfaces.JSONSchema{
				"response": interfaces.JSONSchema{"type": "string"},
			},
		},
		Tools:    interfaces.ToolsToSpecs(tools),
		Messages: input.Messages,
	}

	isLLMStreamSupported := aw.config.LLMClient.IsStreamSupported()
	if !isLLMStreamSupported {
		logger.Debug("llm does not support streaming, falling back to generate")
		resp, err := aw.config.LLMClient.Generate(ctx, req)
		if err != nil {
			return nil, err
		}
		return aw.llmResponseToResult(resp, tools)
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
		upd := &AgentEventUpdate{AgentWorkflowID: input.AgentWorkflowID, Event: ev}
		if input.EventWorkflowID != "" {
			_, _ = aw.config.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
				WorkflowID:   input.EventWorkflowID,
				UpdateName:   agentEventName,
				Args:         []interface{}{upd},
				WaitForStage: client.WorkflowUpdateStageAccepted,
			})
		} else if aw.agentChannel != nil {
			data, _ := json.Marshal(ev)
			channel := agentEventChannelPrefix + input.AgentWorkflowID
			_ = aw.agentChannel.Publish(ctx, channel, data)
		}
	}

	for stream.Next() {
		chunk := stream.Current()
		if chunk == nil {
			continue
		}
		if chunk.ContentDelta != "" {
			emitDelta(&AgentEvent{Type: AgentEventContentDelta, Content: chunk.ContentDelta, Timestamp: time.Now()})
		}
		if chunk.ThinkingDelta != "" {
			emitDelta(&AgentEvent{Type: AgentEventThinkingDelta, Content: chunk.ThinkingDelta, Timestamp: time.Now()})
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	resp := stream.GetResult()
	if resp == nil {
		return nil, fmt.Errorf("stream completed without result")
	}
	logger.Debug("agent LLM stream activity completed", zap.String("agentWorkflowID", input.AgentWorkflowID))
	return aw.llmResponseToResult(resp, tools)
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

// AgentLLMActivity calls the LLM with the conversation and returns content plus any tool calls.
func (aw *AgentWorker) AgentLLMActivity(ctx context.Context, messages []interfaces.Message) (*AgentLLMResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("agent LLM activity started", zap.Int("messageCount", len(messages)))
	tools := aw.config.toolsList()
	req := &interfaces.LLMRequest{
		SystemMessage: aw.config.SystemPrompt,
		ResponseFormat: &interfaces.ResponseFormat{
			Type: interfaces.ResponseFormatJSON,
			Name: "AgentResponse",
			Schema: interfaces.JSONSchema{
				"response": interfaces.JSONSchema{"type": "string"},
			},
		},
		Tools:    interfaces.ToolsToSpecs(tools),
		Messages: messages,
	}
	resp, err := aw.config.LLMClient.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	logger.Debug("agent LLM activity completed", zap.Int("messageCount", len(messages)))
	return aw.llmResponseToResult(resp, tools)
}

// AgentToolApprovalActivity blocks until the driver completes it via CompleteActivity.
// Sends approval request: when remoteWorker use UpdateWorkflow; when local use agentChannel.Publish.
func (aw *AgentWorker) AgentToolApprovalActivity(ctx context.Context, eventWorkflowID string, toolName string, args map[string]any) (ApprovalStatus, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("agent tool approval activity started", zap.String("tool", toolName), zap.Bool("viaEventWorkflow", eventWorkflowID != ""))
	info := activity.GetInfo(ctx)
	req := &approvalRequest{
		ApprovalRequest: ApprovalRequest{ToolName: toolName, Args: args},
		AgentWorkflowID: info.WorkflowExecution.ID,
		TaskToken:       info.TaskToken,
	}

	// Route via event workflow when eventWorkflowID is set (Agent sets this when enableRemoteWorkers is true)
	if eventWorkflowID != "" {
		_, err := aw.config.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   eventWorkflowID,
			UpdateName:   toolRunApprovalName,
			Args:         []interface{}{req},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			return ApprovalStatusNone, err
		}
		logger.Debug("approval request sent to event workflow", zap.String("eventWorkflowID", eventWorkflowID), zap.String("tool", toolName))
	} else {
		if aw.agentChannel == nil {
			return ApprovalStatusNone, fmt.Errorf("agentChannel required when eventWorkflowID is empty")
		}
		data, err := json.Marshal(req)
		if err != nil {
			return ApprovalStatusNone, err
		}
		channel := approvalChannelName(req.AgentWorkflowID)
		if err := aw.agentChannel.Publish(ctx, channel, data); err != nil {
			return ApprovalStatusNone, err
		}
		logger.Debug("approval request published to channel", zap.String("channel", channel), zap.String("tool", toolName))
	}
	logger.Debug("approval request sent, waiting for completion", zap.String("tool", toolName))
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
		logger.Debug("send agent event update activity", zap.String("eventType", string(upd.Event.Type)), zap.String("agentWorkflowID", upd.AgentWorkflowID))
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
		logger.Debug("agent event sent to event workflow", zap.String("eventWorkflowID", eventWorkflowID), zap.String("agentWorkflowID", upd.AgentWorkflowID))
	} else {
		if aw.agentChannel == nil {
			return fmt.Errorf("agentChannel required when eventWorkflowID is empty")
		}
		data, err := json.Marshal(upd.Event)
		if err != nil {
			return err
		}
		channel := agentEventChannelPrefix + upd.AgentWorkflowID
		if err := aw.agentChannel.Publish(ctx, channel, data); err != nil {
			return err
		}
		logger.Debug("agent event sent to channel", zap.String("channel", channel), zap.String("agentWorkflowID", upd.AgentWorkflowID))
	}
	logger.Debug("agent event update activity completed", zap.String("agentWorkflowID", upd.AgentWorkflowID))
	return nil
}

// AgentToolExecuteActivity executes a tool by name. Used after approval when required.
func (aw *AgentWorker) AgentToolExecuteActivity(ctx context.Context, toolName string, args map[string]any) (string, error) {
	logger := activity.GetLogger(ctx)
	logger.Debug("agent tool execute activity started", zap.String("tool", toolName), zap.Int("argCount", len(args)))
	tools := aw.config.toolsList()
	for _, t := range tools {
		if t.Name() == toolName {
			result, err := t.Execute(ctx, args)
			if err != nil {
				logger.Warn("tool execution failed", zap.String("tool", toolName), zap.Error(err))
				return "", err
			}
			logger.Debug("agent tool execute activity completed", zap.String("tool", toolName))
			return fmt.Sprintf("%v", result), nil
		}
	}
	logger.Warn("unknown tool", zap.String("tool", toolName))
	return "", fmt.Errorf("unknown tool: %s", toolName)
}

func retryPolicy(maxAttempts int32) *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
		MaximumInterval:    10 * time.Minute,
		MaximumAttempts:    maxAttempts,
	}
}
