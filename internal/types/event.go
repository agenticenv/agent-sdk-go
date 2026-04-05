package types

import "time"

// AgentEventType identifies a streamed agent event kind.
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

// AgentEventAll is the EventTypes sentinel meaning "emit every event type" (JSON "*").
const AgentEventAll AgentEventType = "*"

// AgentEvent is published to subscribers when the agent produces output or errors.
// AgentName identifies which agent in a delegation tree emitted the event (main or sub-agent).
// Stream uses it so AgentEventComplete from a sub-agent does not close the root stream.
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
	WorkflowID string                 `json:"workflow_id,omitempty"` // optional run identifier for correlation (implementation-defined)
}

// ToolApprovalKind classifies what the user is approving (same event type for Stream).
type ToolApprovalKind string

const (
	// ToolApprovalKindTool is a normal tool execution (default when Kind is empty for older payloads).
	ToolApprovalKindTool ToolApprovalKind = "tool"
	// ToolApprovalKindDelegation is approval to run a registered sub-agent (delegate).
	ToolApprovalKindDelegation ToolApprovalKind = "delegation"
)

// ApprovalEvent is the payload for AgentEventApproval (Stream).
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
	SubAgentName string `json:"sub_agent_name,omitempty"`
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
