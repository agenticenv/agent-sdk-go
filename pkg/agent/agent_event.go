package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

var (
	agentEventChannelPrefix      = "agent_event_"
	toolRunApprovalChannelPrefix = "tool_run_approval_"

	agentEventActivityTimeOut     time.Duration = 30 * time.Minute
	agentEventActivityMaxAttempts int32         = 3

	agentEventName              = "agent-event"
	toolRunApprovalName         = "tool-run-approval"
	eventWorkflowIDPrefix       = "event-"
	eventWorkflowCompleteSignal = "complete" // received when agent Close is called
)

// AgentEventUpdate is the payload for agent-event updates when using one event workflow per agent.
// RunID is the run workflow ID; Event is the event to publish.
type AgentEventUpdate struct {
	RunID string      `json:"run_id"`
	Event *AgentEvent `json:"event"`
}

var (
	// maxEventsPerWorkflow: continue-as-new threshold.
	// overflowBuffer: extra events we accept while processing, to avoid losing events during continue-as-new.
	maxEventsPerWorkflow int = 100
	eventOverflowBuffer  int = 50 // accept up to 150 events; 101–150 can arrive while processing 1–100
)

// AgentEventType is the type of agent events
type AgentEventType string

const (
	AgentEventContent      AgentEventType = "content"
	AgentEventContentDelta AgentEventType = "content_delta" // partial token stream
	AgentEventThinking     AgentEventType = "thinking"
	AgentEventThinkingDelta AgentEventType = "thinking_delta" // Anthropic extended thinking stream
	AgentEventToolCall     AgentEventType = "tool_call"
	AgentEventToolResult   AgentEventType = "tool_result"
	AgentEventError        AgentEventType = "error"
	AgentEventComplete     AgentEventType = "complete"
)

// AgentEvent is published to subscribers when the agent produces output or errors.
type AgentEvent struct {
	Type      AgentEventType         `json:"type"`
	Content   string                 `json:"content,omitempty"`
	ToolCall  *ToolCallEvent         `json:"tool_call,omitempty"`
	Error     error                  `json:"error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

type ToolCallStatus string

const (
	ToolCallStatusPending   ToolCallStatus = "pending"
	ToolCallStatusRunning   ToolCallStatus = "running"
	ToolCallStatusCompleted ToolCallStatus = "completed"
	ToolCallStatusFailed    ToolCallStatus = "failed"
)

type ToolCallEvent struct {
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args,omitempty"`
	Result     any            `json:"result,omitempty"`
	Status     ToolCallStatus `json:"status"`
}

// AgentEventWorkflow is one per agent. Receives events and approval requests via workflow updates.
// Each update includes runID so events are published to per-run channels (agent_event_{runID}, approval_{runID}).
// Completes only when it receives the "complete" signal (on agent Close).
func (a *Agent) AgentEventWorkflow(ctx workflow.Context) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("agent event workflow started")

	var noOfEvents, processedCount int
	var options workflow.UpdateHandlerOptions
	options.Validator = func(ctx workflow.Context, upd *AgentEventUpdate) error {
		if noOfEvents >= maxEventsPerWorkflow+eventOverflowBuffer {
			return fmt.Errorf("max events per workflow reached (%d), continue as new", maxEventsPerWorkflow+eventOverflowBuffer)
		}
		return nil
	}

	eventCh := workflow.NewChannel(ctx)
	approvalCh := workflow.NewChannel(ctx)

	err := workflow.SetUpdateHandlerWithOptions(ctx, agentEventName, func(ctx workflow.Context, upd *AgentEventUpdate) error {
		noOfEvents++
		logger.Debug("received agent event", zap.String("runID", upd.RunID), zap.Any("event", upd.Event))
		eventCh.Send(ctx, upd)
		return nil
	}, options)
	if err != nil {
		return fmt.Errorf("failed setting update handler for events: %w", err)
	}

	err = workflow.SetUpdateHandler(ctx, toolRunApprovalName, func(ctx workflow.Context, req *ApprovalRequest) error {
		logger.Debug("received tool-run-approval", zap.String("tool", req.ToolName))
		approvalCh.Send(ctx, req)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed setting update handler for tool-run-approval: %w", err)
	}

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: agentEventActivityTimeOut,
		RetryPolicy:         retryPolicy(agentEventActivityMaxAttempts),
	})

	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var upd *AgentEventUpdate
			eventCh.Receive(ctx, &upd)
			if upd == nil {
				return
			}
			if err := workflow.ExecuteActivity(actCtx, a.EventPublishActivity, upd.RunID, upd.Event).Get(ctx, nil); err != nil {
				logger.Warn("agent event activity failed", zap.Error(err), zap.Any("event", upd.Event))
			}
			processedCount++
		}
	})

	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var req *ApprovalRequest
			approvalCh.Receive(ctx, &req)
			if req == nil {
				return
			}
			runID := req.WorkflowID
			if err := workflow.ExecuteActivity(actCtx, a.ToolRunApprovalPublishActivity, runID, req).Get(ctx, nil); err != nil {
				logger.Warn("approval publish activity failed", zap.Error(err), zap.String("tool", req.ToolName))
			}
		}
	})

	// Listen for "complete" signal (run done or agent Close) to exit gracefully
	var completeReceived bool
	completeCh := workflow.GetSignalChannel(ctx, eventWorkflowCompleteSignal)
	workflow.Go(ctx, func(ctx workflow.Context) {
		var v struct{}
		completeCh.Receive(ctx, &v)
		completeReceived = true
	})

	logger.Debug("waiting for agent events or complete signal...")

	err = workflow.Await(ctx, func() bool {
		if completeReceived {
			return true
		}
		return noOfEvents >= maxEventsPerWorkflow &&
			processedCount == noOfEvents &&
			workflow.AllHandlersFinished(ctx)
	})
	if err != nil {
		return err
	}

	if completeReceived {
		logger.Debug("agent event workflow received complete signal, finishing")
		return nil
	}
	logger.Debug("agent event workflow continue as new")
	return workflow.NewContinueAsNewError(ctx, a.AgentEventWorkflow)
}

// EventPublishActivity publishes an event to the per-run channel agent_event_{runID}.
func (a *Agent) EventPublishActivity(ctx context.Context, runID string, event *AgentEvent) error {
	logger := activity.GetLogger(ctx)
	logger.Debug("agent event activity", zap.String("runID", runID), zap.Any("event", event))
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	channel := agentEventChannelPrefix + runID
	if err := a.messaging.Publish(ctx, channel, data); err != nil {
		return fmt.Errorf("failed to publish agent event: %w", err)
	}
	return nil
}

// ToolRunApprovalPublishActivity publishes an ApprovalRequest to the per-run channel approval_{runID}.
func (a *Agent) ToolRunApprovalPublishActivity(ctx context.Context, runID string, req *ApprovalRequest) error {
	logger := activity.GetLogger(ctx)
	logger.Debug("approval publish activity", zap.String("runID", runID), zap.String("tool", req.ToolName))
	if req == nil {
		return fmt.Errorf("approval request is nil")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	channel := toolRunApprovalChannelPrefix + runID
	if err := a.messaging.Publish(ctx, channel, data); err != nil {
		return fmt.Errorf("failed to publish approval request: %w", err)
	}
	return nil
}

// subscribeToAgentEvents returns a channel that receives AgentEvent from the per-run event channel.
func (a *Agent) subscribeToAgentEvents(ctx context.Context, runID string) (<-chan *AgentEvent, func() error, error) {
	channel := agentEventChannelPrefix + runID
	ch, closeFn, err := a.messaging.Subscribe(ctx, channel)
	if err != nil {
		return nil, nil, err
	}

	eventCh := make(chan *AgentEvent)
	go func() {
		defer close(eventCh)
		for data := range ch {
			var ev AgentEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			eventCh <- &ev
		}
	}()

	return eventCh, closeFn, nil
}
