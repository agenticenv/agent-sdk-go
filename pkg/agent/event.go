package agent

import "github.com/agenticenv/agent-sdk-go/internal/types"

// AgentEventType is the type of agent events
type AgentEventType = types.AgentEventType

const (
	AgentEventContent       AgentEventType = types.AgentEventContent
	AgentEventContentDelta  AgentEventType = types.AgentEventContentDelta
	AgentEventThinking      AgentEventType = types.AgentEventThinking
	AgentEventThinkingDelta AgentEventType = types.AgentEventThinkingDelta
	AgentEventToolCall      AgentEventType = types.AgentEventToolCall
	AgentEventToolResult    AgentEventType = types.AgentEventToolResult
	AgentEventApproval      AgentEventType = types.AgentEventApproval
	AgentEventError         AgentEventType = types.AgentEventError
	AgentEventComplete      AgentEventType = types.AgentEventComplete
)

// AgentEvent is published to subscribers when the agent produces output or errors.
// AgentName identifies which agent in a delegation tree emitted the event (main or sub-agent).
// RunStream uses it so AgentEventComplete from a sub-agent does not close the root stream.
// For AgentEventApproval, the requesting agent is also on AgentName (not duplicated on Approval).
type AgentEvent = types.AgentEvent

// ToolApprovalKind classifies what the user is approving (same event type for RunStream).
type ToolApprovalKind = types.ToolApprovalKind

const (
	// ToolApprovalKindTool is a normal tool execution (default when Kind is empty for older payloads).
	ToolApprovalKindTool ToolApprovalKind = types.ToolApprovalKindTool
	// ToolApprovalKindDelegation is approval to run a registered sub-agent (delegate).
	ToolApprovalKindDelegation ToolApprovalKind = types.ToolApprovalKindDelegation
)

// ApprovalEvent is the payload for AgentEventApproval (RunStream).
// The agent that requested approval is on AgentEvent.AgentName, not repeated here.
// Use with Agent.OnApproval when the user approves or rejects; see streaming examples.
type ApprovalEvent = types.ApprovalEvent

type ToolCallStatus = types.ToolCallStatus

const (
	ToolCallStatusPending   ToolCallStatus = types.ToolCallStatusPending
	ToolCallStatusRunning   ToolCallStatus = types.ToolCallStatusRunning
	ToolCallStatusCompleted ToolCallStatus = types.ToolCallStatusCompleted
	ToolCallStatusFailed    ToolCallStatus = types.ToolCallStatusFailed
)

type ToolCallEvent = types.ToolCallEvent
