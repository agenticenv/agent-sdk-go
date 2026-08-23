package hooks

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

// OnBudgetExceededHookInput is the payload delivered to [OnBudgetExceededHook] each time a
// per-run budget limit is breached, before the configured action (stop or approval) is taken.
type OnBudgetExceededHookInput struct {
	// RunMeta carries read-only execution context for the current run.
	RunMeta RunMeta

	// Kind identifies which limit was breached: tokens or cost_usd.
	Kind types.BudgetExceededKind

	// TotalTokens is the accumulated token count at the time of the breach.
	TotalTokens int64

	// TotalCostUSD is the accumulated estimated cost in US dollars at the time of the breach.
	TotalCostUSD float64

	// Action is the configured OnExceeded action (BudgetStopRun or BudgetWaitForApproval).
	// When ApprovalsExhausted is true the effective action is BudgetStopRun regardless of this value.
	Action types.BudgetExceededAction

	// ApprovalCount is how many BudgetWaitForApproval approvals have been granted so far in
	// this run (before this breach is processed).
	ApprovalCount int

	// ApprovalsExhausted is true when MaxApprovals has been reached and the run will stop
	// regardless of the configured OnExceeded action.
	ApprovalsExhausted bool
}

// OnBudgetExceededHook is called when a per-run budget limit is breached.
// It is fire-and-forget: the return value is ignored. Use it for metrics, alerts, and audit
// logging. Do not use it to modify run behaviour — use OnExceeded actions for that.
type OnBudgetExceededHook func(ctx context.Context, input OnBudgetExceededHookInput)

// RunOnBudgetExceeded fires all OnBudgetExceeded hooks in group registration order.
// Errors are not propagated; the hook is observability-only.
func RunOnBudgetExceeded(ctx context.Context, groups []HookGroup, input OnBudgetExceededHookInput) {
	for _, g := range groups {
		if len(g.Hooks.OnBudgetExceeded) == 0 {
			continue
		}
		groupInput := input
		groupInput.RunMeta.HooksGroup = g.Name
		for _, hook := range g.Hooks.OnBudgetExceeded {
			if hook == nil {
				continue
			}
			hook(ctx, groupInput)
		}
	}
}
