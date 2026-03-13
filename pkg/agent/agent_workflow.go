package agent

import (
	"context"
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
	RunID           string
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
func (a *Agent) AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (*AgentResponse, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("AgentWorkflow started", "Input", input.Input)
	eventWorkflowID := input.EventWorkflowID
	runID := workflow.GetInfo(ctx).WorkflowExecution.ID

	maxIter := a.maxIterations
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
		if eventWorkflowID == "" || ev == nil {
			return
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = workflow.Now(ctx)
		}
		upd := &AgentEventUpdate{RunID: runID, Event: ev}
		_ = workflow.ExecuteActivity(sendEventCtx, a.SendAgentEventUpdateActivity, eventWorkflowID, upd).Get(ctx, nil)
	}

	isLLMStreamSupported := a.LLMClient.IsStreamSupported()

	useStreaming := eventWorkflowID != "" && input.StreamingEnabled && isLLMStreamSupported
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
				RunID:           runID,
			}
			err = workflow.ExecuteActivity(streamActCtx, a.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
		} else {
			err = workflow.ExecuteActivity(actCtx, a.AgentLLMActivity, messages).Get(actCtx, &llmResult)
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
			logger.Info("Max iterations reached so calling the LLM one more time to get the final response", zap.Int("Iteration", iter))
			if useStreaming {
				streamInput := AgentLLMStreamInput{Messages: messages, EventWorkflowID: eventWorkflowID, RunID: runID}
				err = workflow.ExecuteActivity(streamActCtx, a.AgentLLMStreamActivity, streamInput).Get(streamActCtx, &llmResult)
			} else {
				err = workflow.ExecuteActivity(actCtx, a.AgentLLMActivity, messages).Get(actCtx, &llmResult)
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
				logger.Info("Approval required for tool", "ToolName", tc.ToolName, "Args", tc.Args)
				var status ApprovalStatus
				if err := workflow.ExecuteActivity(approvalCtx, a.AgentToolApprovalActivity, eventWorkflowID, tc.ToolName, tc.Args).Get(approvalCtx, &status); err != nil {
					return nil, err
				}
				approvalStatus = status
			}

			var content string
			if approvalStatus == ApprovalStatusApproved {
				var result string
				if err := workflow.ExecuteActivity(execCtx, a.AgentToolExecuteActivity, tc.ToolName, tc.Args).Get(execCtx, &result); err != nil {
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

	logger.Info("AgentWorkflow completed", "LastContent", lastContent)
	return &AgentResponse{
		Content:   lastContent,
		AgentName: a.Name,
		Model:     a.LLMClient.Model(),
		Metadata:  map[string]any{},
	}, nil
}

func (a *Agent) toolsList() []interfaces.Tool {
	if a.toolRegistry != nil {
		return a.toolRegistry.Tools()
	}
	return a.tools
}

func (a *Agent) requiresApproval(t interfaces.Tool) bool {
	return a.toolApprovalPolicy != nil && a.toolApprovalPolicy.RequiresApproval(t)
}

// AgentLLMStreamActivity streams LLM response tokens and emits content_delta/thinking_delta events.
// Falls back to Generate when the client does not support streaming.
func (a *Agent) AgentLLMStreamActivity(ctx context.Context, input AgentLLMStreamInput) (*AgentLLMResult, error) {
	tools := a.toolsList()
	req := &interfaces.LLMRequest{
		SystemMessage: a.SystemPrompt,
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

	isLLMStreamSupported := a.LLMClient.IsStreamSupported()

	if !isLLMStreamSupported {
		resp, err := a.LLMClient.Generate(ctx, req)
		if err != nil {
			return nil, err
		}
		return a.llmResponseToResult(resp, tools)
	}

	stream, err := a.LLMClient.GenerateStream(ctx, req)
	if err != nil {
		return nil, err
	}

	// Emit deltas as they arrive
	emitDelta := func(ev *AgentEvent) {
		if input.EventWorkflowID == "" || ev == nil {
			return
		}
		upd := &AgentEventUpdate{RunID: input.RunID, Event: ev}
		_, _ = a.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   input.EventWorkflowID,
			UpdateName:   agentEventName,
			Args:         []interface{}{upd},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
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
	return a.llmResponseToResult(resp, tools)
}

func (a *Agent) llmResponseToResult(resp *interfaces.LLMResponse, tools []interfaces.Tool) (*AgentLLMResult, error) {
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
		needsApproval := a.requiresApproval(tool)
		if needsApproval && a.approvalHandler == nil {
			return nil, fmt.Errorf("tool %s requires approval but no WithApprovalHandler set", tc.ToolName)
		}
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
func (a *Agent) AgentLLMActivity(ctx context.Context, messages []interfaces.Message) (*AgentLLMResult, error) {
	tools := a.toolsList()
	req := &interfaces.LLMRequest{
		SystemMessage: a.SystemPrompt,
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
	resp, err := a.LLMClient.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	return a.llmResponseToResult(resp, tools)
}

// AgentToolApprovalActivity blocks until the driver completes it via CompleteActivity.
// Sends approval request to the event workflow when eventWorkflowID is set.
func (a *Agent) AgentToolApprovalActivity(ctx context.Context, eventWorkflowID string, toolName string, args map[string]any) (ApprovalStatus, error) {
	info := activity.GetInfo(ctx)
	req := &ApprovalRequest{
		WorkflowID: info.WorkflowExecution.ID,
		RunID:      info.WorkflowExecution.RunID,
		TaskToken:  info.TaskToken,
		ToolName:   toolName,
		Args:       args,
	}
	if eventWorkflowID != "" {
		_, err := a.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID:   eventWorkflowID,
			UpdateName:   toolRunApprovalName,
			Args:         []interface{}{req},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			return ApprovalStatusPending, err
		}
	}
	return ApprovalStatusPending, activity.ErrResultPending
}

// SendAgentEventUpdateActivity sends an AgentEventUpdate to the event workflow via UpdateWorkflow.
func (a *Agent) SendAgentEventUpdateActivity(ctx context.Context, eventWorkflowID string, upd *AgentEventUpdate) error {
	if eventWorkflowID == "" || upd == nil {
		return nil
	}
	_, err := a.temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   eventWorkflowID,
		UpdateName:   agentEventName,
		Args:         []interface{}{upd},
		WaitForStage: client.WorkflowUpdateStageAccepted,
	})
	return err
}

// AgentToolExecuteActivity executes a tool by name. Used after approval when required.
func (a *Agent) AgentToolExecuteActivity(ctx context.Context, toolName string, args map[string]any) (string, error) {
	tools := a.toolsList()
	for _, t := range tools {
		if t.Name() == toolName {
			result, err := t.Execute(ctx, args)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%v", result), nil
		}
	}
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
