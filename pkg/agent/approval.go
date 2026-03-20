package agent

import (
	"context"
	"encoding/base64"
	"fmt"
)

type ApprovalStatus string

const (
	ApprovalStatusNone     ApprovalStatus = "NONE"
	ApprovalStatusPending  ApprovalStatus = "PENDING"
	ApprovalStatusApproved ApprovalStatus = "APPROVED"
	ApprovalStatusRejected ApprovalStatus = "REJECTED"
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
// For RunStream approvals, use OnApproval with the approval event payload instead.
type ApprovalRequest struct {
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
	Respond  ApprovalSender `json:"-"`
}

// OnApproval completes a tool approval when using RunStream. Pass the string from ev.Approval
// (see the streaming examples) along with the chosen status.
func (a *Agent) OnApproval(ctx context.Context, approvalToken string, status ApprovalStatus) error {
	if status != ApprovalStatusApproved && status != ApprovalStatusRejected {
		return fmt.Errorf("invalid approval status: %s", status)
	}
	taskToken, err := base64.StdEncoding.DecodeString(approvalToken)
	if err != nil {
		return fmt.Errorf("invalid approval token: %w", err)
	}
	return a.temporalClient.CompleteActivity(ctx, taskToken, status, nil)
}
