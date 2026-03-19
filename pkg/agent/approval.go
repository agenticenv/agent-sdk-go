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

// ApprovalHandler is called when a tool needs approval. Call send with the result when ready.
// The handler may return immediately after starting async work. Multiple handlers may run
// concurrently when tools are invoked in parallel.
type ApprovalHandler func(ctx context.Context, req *ApprovalRequest, onApproval ApprovalSender)

// ApprovalRequest contains the user-facing details of a pending tool approval.
// Only ToolName and Args are exposed; workflow ID and task token are internal.
type ApprovalRequest struct {
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
}

// OnApproval completes a pending tool approval. Pass the ApprovalToken from AgentEventToolApproval.
// ApprovalToken is base64-encoded; stateless so it works across pods (e.g. Redis pub/sub to UI, approval from any instance).
// Example: a.OnApproval(ctx, ev.Approval.ApprovalToken, status)
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
