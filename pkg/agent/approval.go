package agent

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

// ErrApprovalAlreadyResolved is returned by [AgentStream.Approve] (and the deprecated
// [Agent.OnApproval]) when the approval token refers to an activity task that has already been
// completed (approved or rejected). This can happen when a reconnecting subscriber replays a
// CUSTOM approval event for an approval that was resolved while the subscriber was disconnected.
// Treat it as informational — the run is already advancing.
var ErrApprovalAlreadyResolved = types.ErrApprovalAlreadyResolved

type ApprovalStatus = types.ApprovalStatus

const (
	ApprovalStatusNone        ApprovalStatus = types.ApprovalStatusNone
	ApprovalStatusPending     ApprovalStatus = types.ApprovalStatusPending
	ApprovalStatusApproved    ApprovalStatus = types.ApprovalStatusApproved
	ApprovalStatusRejected    ApprovalStatus = types.ApprovalStatusRejected
	ApprovalStatusUnavailable ApprovalStatus = types.ApprovalStatusUnavailable
	ApprovalStatusTimedOut    ApprovalStatus = types.ApprovalStatusTimedOut
)

// ApprovalSender sends an approval result for the current run. Call once per request. Safe for concurrent use—
// multiple approvals may be pending when tools run in parallel.
type ApprovalSender = types.ApprovalSender

// ApprovalHandler is called for pending tool approval during [Agent.Run].
// Register once at [NewAgent] via [WithApprovalHandler] (construction-time, not per-call).
// req.Respond is always set: call req.Respond(ApprovalStatusApproved) or Rejected when ready.
// The handler may return immediately after starting async work. Multiple invocations may run
// concurrently when tools are invoked in parallel. For streaming approvals use [AgentStream.Approve].
type ApprovalHandler = types.ApprovalHandler

// ApprovalRequestName classifies approval callbacks (aligned with CUSTOM event roles).
type ApprovalRequestName = types.ApprovalRequestName

const (
	ApprovalRequestNameTool     = types.ApprovalRequestNameTool
	ApprovalRequestNameSubAgent = types.ApprovalRequestNameSubAgent
	ApprovalRequestNameBudget   = types.ApprovalRequestNameBudget
)

// ApprovalRequest describes a pending approval for [Agent.Run].
// Name + Value mirror CUSTOM stream events; use [ParseToolApproval] / [ParseDelegationApproval] / [ParseBudgetApproval].
// Respond is always set; call it once with ApprovalStatusApproved or ApprovalStatusRejected.
// For streaming approvals, use [AgentStream.Approve] with the approval token from the CUSTOM event Value.
type ApprovalRequest = types.ApprovalRequest

// ToolApprovalRequestValue is the decoded Value for tool approvals (matches CUSTOM approval payload).
type ToolApprovalRequestValue = types.ToolApprovalRequestValue

// SubAgentDelegationApprovalRequestValue is the decoded Value for delegation approvals.
type SubAgentDelegationApprovalRequestValue = types.SubAgentDelegationApprovalRequestValue

// BudgetApprovalRequestValue is the decoded Value for budget approvals.
type BudgetApprovalRequestValue = types.BudgetApprovalRequestValue

// ParseToolApproval decodes Value when Name is [ApprovalRequestNameTool] (handles map[string]any from JSON).
func ParseToolApproval(req *ApprovalRequest) (ToolApprovalRequestValue, error) {
	return types.ParseToolApproval(req)
}

// ParseDelegationApproval decodes Value when Name is [ApprovalRequestNameSubAgent].
func ParseDelegationApproval(req *ApprovalRequest) (SubAgentDelegationApprovalRequestValue, error) {
	return types.ParseDelegationApproval(req)
}

// ParseBudgetApproval decodes Value when Name is [ApprovalRequestNameBudget].
func ParseBudgetApproval(req *ApprovalRequest) (BudgetApprovalRequestValue, error) {
	return types.ParseBudgetApproval(req)
}

// OnApproval completes a pending tool or delegation approval when using [Agent.Stream].
// Pass the approval token from the CUSTOM event Value and [ApprovalStatusApproved] or
// [ApprovalStatusRejected].
//
// Deprecated: Use [AgentStream.Approve] on the handle from [Agent.Stream] or
// [Agent.GetAgentStream] instead. This method will be removed in v0.4.0.
func (a *Agent) OnApproval(ctx context.Context, approvalToken string, status ApprovalStatus) error {
	return a.runtime.OnApproval(ctx, approvalToken, status)
}
