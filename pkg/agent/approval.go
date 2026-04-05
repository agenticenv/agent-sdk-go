package agent

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

type ApprovalStatus = types.ApprovalStatus

const (
	ApprovalStatusNone     ApprovalStatus = types.ApprovalStatusNone
	ApprovalStatusPending  ApprovalStatus = types.ApprovalStatusPending
	ApprovalStatusApproved ApprovalStatus = types.ApprovalStatusApproved
	ApprovalStatusRejected ApprovalStatus = types.ApprovalStatusRejected
)

// ApprovalSender sends an approval result. Call once per request. Safe for concurrent use—
// multiple approvals may be pending when tools run in parallel.
type ApprovalSender = types.ApprovalSender

// ApprovalHandler is called when a tool needs approval (Run with WithApprovalHandler).
// req.Respond is always set: call req.Respond(ApprovalStatusApproved) or Rejected when ready.
// The handler may return immediately after starting async work. Multiple invocations may run
// concurrently when tools are invoked in parallel.
type ApprovalHandler = types.ApprovalHandler

// ApprovalRequest describes a pending tool approval for Run and RunAsync.
// Respond is always set; call it once with ApprovalStatusApproved or ApprovalStatusRejected.
// For RunStream approvals, use OnApproval with the approval event payload instead.
type ApprovalRequest = types.ApprovalRequest

// OnApproval completes a tool approval when using RunStream. Pass the string from ev.Approval
// (see the streaming examples) along with the chosen status.
func (a *Agent) OnApproval(ctx context.Context, approvalToken string, status ApprovalStatus) error {
	return a.runtime.Approve(ctx, approvalToken, status)
}
