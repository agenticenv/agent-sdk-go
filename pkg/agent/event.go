package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"log/slog"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

var (
	agentEventChannelPrefix = "agent_event_"

	agentEventActivityTaskTimeout time.Duration = 2 * time.Minute
	agentEventActivityMaxAttempts int32         = 3

	agentEventName              = "agent-event"
	eventWorkflowCompleteSignal = "complete" // received when agent Close is called
)

// eventChannelName returns the pub/sub channel name for agent events. runID is the run workflow ID.
func eventChannelName(runID string) string {
	return agentEventChannelPrefix + runID
}

// AgentEventUpdate is the payload for agent-event updates when using one event workflow per agent.
// AgentName is the name of the agent that emitted the event (main agent or a sub-agent).
// LocalChannelName is the in-process pub/sub channel name (agent_event_<main-workflow-id>)
// all nodes in the delegation tree publish to.
type AgentEventUpdate struct {
	AgentName        string      `json:"agent_name"`
	LocalChannelName string      `json:"local_channel_name,omitempty"`
	Event            *AgentEvent `json:"event"`
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
	AgentEventContent       AgentEventType = "content"
	AgentEventContentDelta  AgentEventType = "content_delta" // partial token stream
	AgentEventThinking      AgentEventType = "thinking"
	AgentEventThinkingDelta AgentEventType = "thinking_delta" // Anthropic extended thinking stream
	AgentEventToolCall      AgentEventType = "tool_call"
	AgentEventToolResult    AgentEventType = "tool_result"
	AgentEventApproval      AgentEventType = "approval"
	AgentEventError         AgentEventType = "error"
	AgentEventComplete      AgentEventType = "complete"
)

// agentEventAll is the workflow EventTypes sentinel meaning "emit every event type" (JSON "*").
// Unexported so it is not part of the public SDK surface.
const agentEventAll AgentEventType = "*"

// AgentEvent is published to subscribers when the agent produces output or errors.
// AgentName identifies which agent in a delegation tree emitted the event (main or sub-agent).
// RunStream uses it so AgentEventComplete from a sub-agent does not close the root stream.
// For AgentEventApproval, the requesting agent is also on AgentName (not duplicated on Approval).
type AgentEvent struct {
	Type       AgentEventType         `json:"type"`
	AgentName  string                 `json:"agent_name,omitempty"`
	Content    string                 `json:"content,omitempty"`
	ToolCall   *ToolCallEvent         `json:"tool_call,omitempty"`
	Approval   *ApprovalEvent         `json:"approval,omitempty"` // for AgentEventApproval
	Error      error                  `json:"error,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Timestamp  time.Time              `json:"timestamp"`
	WorkflowID string                 `json:"workflow_id,omitempty"` // run workflow ID; for correlation only
}

// ToolApprovalKind classifies what the user is approving (same event type for RunStream).
type ToolApprovalKind string

const (
	// ToolApprovalKindTool is a normal tool execution (default when Kind is empty for older payloads).
	ToolApprovalKindTool ToolApprovalKind = "tool"
	// ToolApprovalKindDelegation is approval to run a registered sub-agent (delegate).
	ToolApprovalKindDelegation ToolApprovalKind = "delegation"
)

// ApprovalEvent is the payload for AgentEventApproval (RunStream).
// The agent that requested approval is on AgentEvent.AgentName, not repeated here.
// Use with Agent.OnApproval when the user approves or rejects; see streaming examples.
type ApprovalEvent struct {
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	ToolName      string         `json:"tool_name"`
	Args          map[string]any `json:"args,omitempty"`
	ApprovalToken string         `json:"approval_token,omitempty"`
	// Kind is tool vs sub-agent delegation; use for UI copy.
	Kind ToolApprovalKind `json:"kind,omitempty"`
	// DelegateToName is set when Kind is delegation: display name of the target sub-agent.
	DelegateToName string `json:"delegate_to_name,omitempty"`
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

	err := workflow.SetUpdateHandlerWithOptions(ctx, agentEventName, func(ctx workflow.Context, upd *AgentEventUpdate) error {
		noOfEvents++
		evTypeStr := ""
		if upd.Event != nil {
			evTypeStr = string(upd.Event.Type)
		}
		logger.Debug("received agent event", "agent", upd.AgentName, "eventType", evTypeStr)
		eventCh.Send(ctx, upd)
		return nil
	}, options)
	if err != nil {
		return fmt.Errorf("failed setting update handler for events: %w", err)
	}

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: agentEventActivityTaskTimeout,
		RetryPolicy:         retryPolicy(agentEventActivityMaxAttempts),
	})

	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var upd *AgentEventUpdate
			eventCh.Receive(ctx, &upd)
			if upd == nil {
				return
			}
			if err := workflow.ExecuteActivity(actCtx, a.EventPublishActivity, upd.LocalChannelName, upd.Event).Get(ctx, nil); err != nil {
				evType := ""
				if upd.Event != nil {
					evType = string(upd.Event.Type)
				}
				logger.Warn("agent event activity failed", "error", err, "eventType", evType, "agent", upd.AgentName)
			}
			processedCount++
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

// EventPublishActivity publishes an event to the given channel (agent_event_<main-workflow-id>).
func (a *Agent) EventPublishActivity(ctx context.Context, channel string, event *AgentEvent) error {
	logger := activity.GetLogger(ctx)
	evType := ""
	if event != nil {
		evType = string(event.Type)
	}
	logger.Debug("agent event activity", "channel", channel, "eventType", evType)
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := a.eventbus.Publish(ctx, channel, data); err != nil {
		logger.Error("failed to publish agent event", "channel", channel, "error", err)
		return fmt.Errorf("failed to publish agent event: %w", err)
	}
	return nil
}

// subscribeToAgentEvents returns a channel that receives AgentEvent from the given event channel.
func (a *Agent) subscribeToAgentEvents(ctx context.Context, channel string) (<-chan *AgentEvent, func() error, error) {
	a.logger.Debug(ctx, "subscribing to agent events", slog.String("channel", channel))
	ch, closeFn, err := a.eventbus.Subscribe(ctx, channel)
	if err != nil {
		a.logger.Error(ctx, "failed to subscribe to agent events", slog.String("channel", channel), slog.Any("error", err))
		return nil, nil, err
	}

	eventCh := make(chan *AgentEvent)
	go func() {
		defer close(eventCh)
		for data := range ch {
			var ev AgentEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				a.logger.Debug(ctx, "failed to unmarshal agent event", slog.Any("error", err))
				continue
			}
			eventCh <- &ev
		}
	}()

	a.logger.Debug(ctx, "subscribed to agent events", slog.String("channel", channel))
	return eventCh, closeFn, nil
}
