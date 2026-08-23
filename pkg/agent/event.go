package agent

import "github.com/agenticenv/agent-sdk-go/internal/events"

//go:generate mockgen -destination=./mocks/mock_event.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/agent AgentEvent

// AgentEventType identifies a streamed event kind from the execution runtime.
type AgentEventType = events.AgentEventType

const (
	AgentEventTypeRunStarted   AgentEventType = events.AgentEventTypeRunStarted
	AgentEventTypeRunFinished  AgentEventType = events.AgentEventTypeRunFinished
	AgentEventTypeRunError     AgentEventType = events.AgentEventTypeRunError
	AgentEventTypeStepStarted  AgentEventType = events.AgentEventTypeStepStarted
	AgentEventTypeStepFinished AgentEventType = events.AgentEventTypeStepFinished

	AgentEventTypeTextMessageStart   AgentEventType = events.AgentEventTypeTextMessageStart
	AgentEventTypeTextMessageContent AgentEventType = events.AgentEventTypeTextMessageContent
	AgentEventTypeTextMessageEnd     AgentEventType = events.AgentEventTypeTextMessageEnd

	AgentEventTypeToolCallStart  AgentEventType = events.AgentEventTypeToolCallStart
	AgentEventTypeToolCallArgs   AgentEventType = events.AgentEventTypeToolCallArgs
	AgentEventTypeToolCallEnd    AgentEventType = events.AgentEventTypeToolCallEnd
	AgentEventTypeToolCallResult AgentEventType = events.AgentEventTypeToolCallResult

	AgentEventTypeReasoningStart          AgentEventType = events.AgentEventTypeReasoningStart
	AgentEventTypeReasoningMessageStart   AgentEventType = events.AgentEventTypeReasoningMessageStart
	AgentEventTypeReasoningMessageContent AgentEventType = events.AgentEventTypeReasoningMessageContent
	AgentEventTypeReasoningMessageEnd     AgentEventType = events.AgentEventTypeReasoningMessageEnd
	AgentEventTypeReasoningEnd            AgentEventType = events.AgentEventTypeReasoningEnd

	AgentEventTypeRaw    AgentEventType = events.AgentEventTypeRaw
	AgentEventTypeCustom AgentEventType = events.AgentEventTypeCustom
)

// AgentEvent is the interface for all agent events.
type AgentEvent = events.AgentEvent

// BaseEvent is published to subscribers when the agent produces output or errors during a run.
// AgentName identifies which agent in a delegation tree emitted the event (main or sub-agent).
// [Agent.Stream] uses it so [AgentEventTypeRunFinished] from a sub-agent does not close the root stream.
// For [AgentEventTypeCustom], the requesting agent is also on AgentName (not duplicated on Approval).
type BaseEvent = events.BaseEvent

// AgentRunStartedEvent is published to subscribers when the agent starts a run.
type AgentRunStartedEvent = events.AgentRunStartedEvent

// AgentRunFinishedEvent is published to subscribers when the agent finishes a run.
type AgentRunFinishedEvent = events.AgentRunFinishedEvent

// AgentRunErrorEvent is published to subscribers when the agent encounters an error.
type AgentRunErrorEvent = events.AgentRunErrorEvent

// AgentStepStartedEvent is published when a step begins. It is emitted when a sub-agent
// child workflow is about to run; StepName is the sub-agent route name (WithSubAgents / SubAgentRoute.Name).
type AgentStepStartedEvent = events.AgentStepStartedEvent

// AgentStepFinishedEvent is published when that step ends. For sub-agent runs, emitted
// after the child workflow returns, success or failure (the tool result may still contain an error string).
type AgentStepFinishedEvent = events.AgentStepFinishedEvent

// AgentTextMessageStartEvent is published to subscribers when the agent starts a text message.
type AgentTextMessageStartEvent = events.AgentTextMessageStartEvent

// AgentTextMessageContentEvent is published to subscribers when the agent sends a text message content.
type AgentTextMessageContentEvent = events.AgentTextMessageContentEvent

// AgentTextMessageEndEvent is published to subscribers when the agent ends a text message.
type AgentTextMessageEndEvent = events.AgentTextMessageEndEvent

// AgentToolCallStartEvent is published to subscribers when the agent starts a tool call.
type AgentToolCallStartEvent = events.AgentToolCallStartEvent

// AgentToolCallArgsEvent is published to subscribers when the agent sends a tool call args.
type AgentToolCallArgsEvent = events.AgentToolCallArgsEvent

// AgentToolCallEndEvent is published to subscribers when the agent ends a tool call.
type AgentToolCallEndEvent = events.AgentToolCallEndEvent

// AgentToolCallResultEvent is published to subscribers when the agent sends a tool call result.
type AgentToolCallResultEvent = events.AgentToolCallResultEvent

// AgentReasoningStartEvent is published to subscribers when the agent starts reasoning.
type AgentReasoningStartEvent = events.AgentReasoningStartEvent

// AgentReasoningMessageStartEvent is published to subscribers when the agent starts a reasoning message.
type AgentReasoningMessageStartEvent = events.AgentReasoningMessageStartEvent

// AgentReasoningMessageContentEvent is published to subscribers when the agent sends a reasoning message content.
type AgentReasoningMessageContentEvent = events.AgentReasoningMessageContentEvent

// AgentReasoningMessageEndEvent is published to subscribers when the agent ends a reasoning message.
type AgentReasoningMessageEndEvent = events.AgentReasoningMessageEndEvent

// AgentReasoningEndEvent is published to subscribers when the agent ends reasoning.
type AgentReasoningEndEvent = events.AgentReasoningEndEvent

// AgentRawEvent is published to subscribers when the agent sends a raw event.
type AgentRawEvent = events.AgentRawEvent

// AgentCustomEvent is published to subscribers when the agent sends a custom event.
type AgentCustomEvent = events.AgentCustomEvent

// AgentCustomEventName classifies what the user is approving when using streaming or approval events.
type AgentCustomEventName = events.AgentCustomEventName

const (
	// AgentCustomEventNameToolApproval is a normal tool execution (default when Kind is empty for older payloads).
	AgentCustomEventNameToolApproval AgentCustomEventName = events.AgentCustomEventNameToolApproval
	// AgentCustomEventNameSubAgentDelegation is approval to run a registered sub-agent (delegate).
	AgentCustomEventNameSubAgentDelegation AgentCustomEventName = events.AgentCustomEventNameSubAgentDelegation
	// AgentCustomEventNameBudget is a per-run budget limit pause.
	AgentCustomEventNameBudget AgentCustomEventName = events.AgentCustomEventNameBudget
)

// AgentCustomEventApprovalValue is the value of the custom event for tool approval.
type AgentCustomEventApprovalValue = events.AgentCustomEventApprovalValue

// AgentCustomEventDelegationValue is the value of the custom event for sub-agent delegation.
type AgentCustomEventDelegationValue = events.AgentCustomEventDelegationValue

// AgentCustomEventBudgetValue is the value of the custom event for budget approval.
type AgentCustomEventBudgetValue = events.AgentCustomEventBudgetValue

// ParseCustomEventApproval returns the typed value for CUSTOM events with name [AgentCustomEventNameToolApproval].
// Use this when handling stream events: JSON decode leaves Value as map[string]any.
func ParseCustomEventApproval(ev *AgentCustomEvent) (AgentCustomEventApprovalValue, error) {
	return events.ParseCustomEventApproval(ev)
}

// ParseCustomEventDelegation returns the typed value for CUSTOM events with name [AgentCustomEventNameSubAgentDelegation].
func ParseCustomEventDelegation(ev *AgentCustomEvent) (AgentCustomEventDelegationValue, error) {
	return events.ParseCustomEventDelegation(ev)
}

// ParseCustomEventBudget returns the typed value for CUSTOM events with name [AgentCustomEventNameBudget].
func ParseCustomEventBudget(ev *AgentCustomEvent) (AgentCustomEventBudgetValue, error) {
	return events.ParseCustomEventBudget(ev)
}
