package types

import (
	"context"
	"time"
)

// maxApprovalTimeout caps how long a single approval wait may last in the runtime.
const MaxApprovalTimeout = 31 * 24 * time.Hour

type ApprovalStatus string

const (
	ApprovalStatusNone     ApprovalStatus = "NONE"
	ApprovalStatusPending  ApprovalStatus = "PENDING"
	ApprovalStatusApproved ApprovalStatus = "APPROVED"
	ApprovalStatusRejected ApprovalStatus = "REJECTED"
	// ApprovalStatusUnavailable means the approval request could not be delivered (e.g. event stream down). It is not a user rejection.
	ApprovalStatusUnavailable ApprovalStatus = "UNAVAILABLE"
)

// ApprovalSender sends an approval result. Call once per request. Safe for concurrent use—
// multiple approvals may be pending when tools run in parallel.
type ApprovalSender func(status ApprovalStatus) error

// ApprovalHandler is called when a tool needs approval (Run with WithApprovalHandler).
// req.Respond is always set: call req.Respond(ApprovalStatusApproved) or Rejected when ready.
// The handler may return immediately after starting async work. Multiple invocations may run
// concurrently when tools are invoked in parallel.
type ApprovalHandler func(ctx context.Context, req *ApprovalRequest)

// ApprovalRequest describes a pending tool approval for Run and RunAsync.
// Respond is always set; call it once with ApprovalStatusApproved or ApprovalStatusRejected.
// For Stream approvals, use OnApproval with the approval event payload instead.
type ApprovalRequest struct {
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
	Respond  ApprovalSender `json:"-"`
	// Kind matches ApprovalEvent: distinguish normal tools from sub-agent delegation.
	Kind ToolApprovalKind `json:"kind,omitempty"`
	// AgentName is the agent that requested approval for the current run.
	AgentName string `json:"agent_name,omitempty"`
	// SubAgentName is set for delegation: human-friendly target specialist name.
	SubAgentName string `json:"sub_agent_name,omitempty"`
}
