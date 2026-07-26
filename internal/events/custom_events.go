package events

import (
	"encoding/json"
	"fmt"
)

// RAW
type AgentRawEvent struct {
	*BaseEvent
	Event  any    `json:"event"`
	Source string `json:"source,omitempty"`
}

func NewAgentRawEvent(event any, source ...string) *AgentRawEvent {
	e := &AgentRawEvent{
		BaseEvent: NewBaseEvent(AgentEventTypeRaw),
		Event:     event,
	}
	if len(source) > 0 {
		e.Source = source[0]
	}
	return e
}

func (e *AgentRawEvent) ToJSON() ([]byte, error) { return json.Marshal(e) }

// CUSTOM
type AgentCustomEvent struct {
	*BaseEvent
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
}

func NewAgentCustomEvent(name string, value any) *AgentCustomEvent {
	return &AgentCustomEvent{
		BaseEvent: NewBaseEvent(AgentEventTypeCustom),
		Name:      name,
		Value:     value,
	}
}

func (e *AgentCustomEvent) ToJSON() ([]byte, error) { return json.Marshal(e) }

// AgentCustomEventName is the CUSTOM event name discriminator (value is JSON-specific).
type AgentCustomEventName string

const (
	AgentCustomEventNameToolApproval       AgentCustomEventName = "tool_approval"
	AgentCustomEventNameSubAgentDelegation AgentCustomEventName = "sub_agent_delegation"
)

// AgentCustomEventApprovalValue is the JSON shape for CUSTOM name=approval (tool or delegation; use Kind).
type AgentCustomEventApprovalValue struct {
	AgentName       string         `json:"agentName,omitempty"`
	ToolCallID      string         `json:"toolCallId,omitempty"`
	ToolName        string         `json:"toolName"`
	ToolDisplayName string         `json:"toolDisplayName,omitempty"`
	Args            map[string]any `json:"args,omitempty"`
	ApprovalToken   string         `json:"approvalToken,omitempty"`
}

func NewAgentCustomEventApprovalValue(toolName, approvalToken string) *AgentCustomEventApprovalValue {
	return &AgentCustomEventApprovalValue{
		ToolName:      toolName,
		ApprovalToken: approvalToken,
	}
}

func (v *AgentCustomEventApprovalValue) ToJSON() ([]byte, error) { return json.Marshal(v) }

// AgentCustomEventDelegationValue is the JSON shape for CUSTOM name=delegation (subset when using a dedicated name).
type AgentCustomEventDelegationValue struct {
	AgentName     string         `json:"agentName,omitempty"`
	SubAgentName  string         `json:"subAgentName,omitempty"`
	ToolCallID    string         `json:"toolCallId,omitempty"` // workflow-side ID; used for pending-approval tracking on Events/reconnect
	Args          map[string]any `json:"args,omitempty"`
	ApprovalToken string         `json:"approvalToken,omitempty"`
}

func NewAgentCustomEventDelegationValue(subAgentName, approvalToken string) *AgentCustomEventDelegationValue {
	return &AgentCustomEventDelegationValue{
		SubAgentName:  subAgentName,
		ApprovalToken: approvalToken,
	}
}

func (v *AgentCustomEventDelegationValue) ToJSON() ([]byte, error) { return json.Marshal(v) }

func parseCustomPayload[V any](ev *AgentCustomEvent) (v V, err error) {
	if ev == nil {
		return v, fmt.Errorf("events: nil custom event")
	}
	switch x := ev.Value.(type) {
	case V:
		return x, nil
	case *V:
		if x == nil {
			return v, fmt.Errorf("events: nil custom value pointer")
		}
		return *x, nil
	default:
		raw, mErr := json.Marshal(ev.Value)
		if mErr != nil {
			return v, fmt.Errorf("events: marshal custom value: %w", mErr)
		}
		if uErr := json.Unmarshal(raw, &v); uErr != nil {
			return v, fmt.Errorf("events: unmarshal custom value: %w", uErr)
		}
		return v, nil
	}
}

// ParseCustomEventApproval returns the typed value field for CUSTOM events with name "approval"
// (after EventFromJSON or bus decode, Value is often map[string]any).
func ParseCustomEventApproval(ev *AgentCustomEvent) (AgentCustomEventApprovalValue, error) {
	return parseCustomPayload[AgentCustomEventApprovalValue](ev)
}

// ParseCustomEventDelegation returns the typed value field for CUSTOM events with name "delegation".
func ParseCustomEventDelegation(ev *AgentCustomEvent) (AgentCustomEventDelegationValue, error) {
	return parseCustomPayload[AgentCustomEventDelegationValue](ev)
}
