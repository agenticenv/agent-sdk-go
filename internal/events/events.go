package events

import (
	"encoding/json"
	"fmt"
	"time"
)

// AgentEventType is the AG-UI protocol discriminator used on the wire.
type AgentEventType string

// AgentEventAll is the EventTypes filter sentinel meaning emit every event kind (JSON "*"). Internal use only.
const AgentEventAll AgentEventType = "*"

// AgentEventType values are the AG-UI protocol discriminator used on the wire.
// Ref: https://docs.ag-ui.com/sdk/js/core/events#events
const (
	// Lifecycle (supported now)
	// --- AG-UI lifecycle events ---
	AgentEventTypeRunStarted   AgentEventType = "RUN_STARTED"
	AgentEventTypeRunFinished  AgentEventType = "RUN_FINISHED"
	AgentEventTypeRunError     AgentEventType = "RUN_ERROR"
	AgentEventTypeStepStarted  AgentEventType = "STEP_STARTED"
	AgentEventTypeStepFinished AgentEventType = "STEP_FINISHED"

	// Text messages (supported now)
	// --- AG-UI text events ---
	AgentEventTypeTextMessageStart   AgentEventType = "TEXT_MESSAGE_START"
	AgentEventTypeTextMessageContent AgentEventType = "TEXT_MESSAGE_CONTENT"
	AgentEventTypeTextMessageEnd     AgentEventType = "TEXT_MESSAGE_END"
	//AgentEventTypeTextMessageChunk   AgentEventType = "TEXT_MESSAGE_CHUNK" // reserved for later

	// Tool calls (supported now)
	// --- AG-UI tool events ---
	AgentEventTypeToolCallStart  AgentEventType = "TOOL_CALL_START"
	AgentEventTypeToolCallArgs   AgentEventType = "TOOL_CALL_ARGS"
	AgentEventTypeToolCallEnd    AgentEventType = "TOOL_CALL_END"
	AgentEventTypeToolCallResult AgentEventType = "TOOL_CALL_RESULT"
	//AgentEventTypeToolCallChunk  AgentEventType = "TOOL_CALL_CHUNK" // reserved for later

	// State & messages & activity
	// --- AG-UI state & messages & activity events ---
	//AgentEventTypeStateSnapshot    AgentEventType = "STATE_SNAPSHOT"
	//AgentEventTypeStateDelta       AgentEventType = "STATE_DELTA"
	//AgentEventTypeMessagesSnapshot AgentEventType = "MESSAGES_SNAPSHOT"
	//AgentEventTypeActivitySnapshot AgentEventType = "ACTIVITY_SNAPSHOT"
	//AgentEventTypeActivityDelta    AgentEventType = "ACTIVITY_DELTA"

	// Reasoning
	// --- AG-UI reasoning events ---
	AgentEventTypeReasoningStart          AgentEventType = "REASONING_START"
	AgentEventTypeReasoningMessageStart   AgentEventType = "REASONING_MESSAGE_START"
	AgentEventTypeReasoningMessageContent AgentEventType = "REASONING_MESSAGE_CONTENT"
	AgentEventTypeReasoningMessageEnd     AgentEventType = "REASONING_MESSAGE_END"
	//AgentEventTypeReasoningMessageChunk   AgentEventType = "REASONING_MESSAGE_CHUNK"   // reserved for later
	AgentEventTypeReasoningEnd AgentEventType = "REASONING_END" // reserved for later
	//AgentEventTypeReasoningEncryptedValue AgentEventType = "REASONING_ENCRYPTED_VALUE" // reserved for later

	// Raw & custom events
	// --- AG-UI custom events ---
	AgentEventTypeRaw    AgentEventType = "RAW"
	AgentEventTypeCustom AgentEventType = "CUSTOM"
)

// AgentEvent is the interface for all agent events.
type AgentEvent interface {
	Type() AgentEventType    // for Go switch
	Timestamp() *int64       // for ordering
	ToJSON() ([]byte, error) // for cross-language transport
}

// BaseEvent is AG-UI BaseEvent; RawEvent maps to JSON rawEvent when present.
// The offset fields are NOT serialised to JSON to preserve wire compatibility with
// the AG-UI protocol; they carry the workflowstreams global offset for reconnect
// support (see Agent.GetAgentStream / AgentStream.Events with WithOffset) and are set by the stream forwarder after decode.
type BaseEvent struct {
	EventType      AgentEventType `json:"type"`
	EventTimestamp *int64         `json:"timestamp,omitempty"`

	// offset is the workflowstreams global offset at which this event was appended.
	// hasOffset is false until SetOffset is called (zero-value is ambiguous without the flag).
	offset    int64
	hasOffset bool
}

func NewBaseEvent(t AgentEventType) *BaseEvent {
	now := time.Now().UnixMilli()
	return &BaseEvent{EventType: t, EventTimestamp: &now}
}

func (b *BaseEvent) Type() AgentEventType    { return b.EventType }
func (b *BaseEvent) Timestamp() *int64       { return b.EventTimestamp }
func (b *BaseEvent) ToJSON() ([]byte, error) { return json.Marshal(b) }

// SetOffset records the workflowstreams global offset for this event.
// Called by the stream forwarder after decoding a subscribed item.
func (b *BaseEvent) SetOffset(v int64) {
	b.offset = v
	b.hasOffset = true
}

// Offset returns the workflowstreams offset and whether it was set.
// Pass the returned offset to AgentStream.Events(WithOffset) to resume the stream from this point.
func (b *BaseEvent) Offset() (int64, bool) {
	return b.offset, b.hasOffset
}

var eventRegistry = map[AgentEventType]func() AgentEvent{
	AgentEventTypeRunStarted:   func() AgentEvent { return &AgentRunStartedEvent{} },
	AgentEventTypeRunFinished:  func() AgentEvent { return &AgentRunFinishedEvent{} },
	AgentEventTypeRunError:     func() AgentEvent { return &AgentRunErrorEvent{} },
	AgentEventTypeStepStarted:  func() AgentEvent { return &AgentStepStartedEvent{} },
	AgentEventTypeStepFinished: func() AgentEvent { return &AgentStepFinishedEvent{} },

	AgentEventTypeTextMessageStart:   func() AgentEvent { return &AgentTextMessageStartEvent{} },
	AgentEventTypeTextMessageContent: func() AgentEvent { return &AgentTextMessageContentEvent{} },
	AgentEventTypeTextMessageEnd:     func() AgentEvent { return &AgentTextMessageEndEvent{} },

	AgentEventTypeToolCallStart:  func() AgentEvent { return &AgentToolCallStartEvent{} },
	AgentEventTypeToolCallArgs:   func() AgentEvent { return &AgentToolCallArgsEvent{} },
	AgentEventTypeToolCallEnd:    func() AgentEvent { return &AgentToolCallEndEvent{} },
	AgentEventTypeToolCallResult: func() AgentEvent { return &AgentToolCallResultEvent{} },

	AgentEventTypeReasoningMessageContent: func() AgentEvent { return &AgentReasoningMessageContentEvent{} },
	AgentEventTypeReasoningMessageStart:   func() AgentEvent { return &AgentReasoningMessageStartEvent{} },
	AgentEventTypeReasoningMessageEnd:     func() AgentEvent { return &AgentReasoningMessageEndEvent{} },
	AgentEventTypeReasoningStart:          func() AgentEvent { return &AgentReasoningStartEvent{} },
	AgentEventTypeReasoningEnd:            func() AgentEvent { return &AgentReasoningEndEvent{} },

	AgentEventTypeRaw:    func() AgentEvent { return &AgentRawEvent{} },
	AgentEventTypeCustom: func() AgentEvent { return &AgentCustomEvent{} },
}

func EventTypeFromJSON(data []byte) (AgentEventType, error) {
	var base struct {
		Type AgentEventType `json:"type"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return "", err
	}
	return base.Type, nil
}

func EventFromJSON(data []byte) (AgentEvent, error) {
	eventType, err := EventTypeFromJSON(data)
	if err != nil {
		return nil, err
	}

	factory, ok := eventRegistry[eventType]
	if !ok {
		return nil, fmt.Errorf("unknown event type: %s", eventType)
	}

	event := factory()
	if err := json.Unmarshal(data, event); err != nil {
		return nil, err
	}
	return event, nil
}
