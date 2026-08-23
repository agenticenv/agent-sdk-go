package types

// BudgetExceededAction is the action taken when a per-run budget limit is reached.
// Budget limits apply per run and reset when a new run starts.
type BudgetExceededAction string

const (
	// BudgetStopRun stops the run immediately and returns an error to the caller.
	// This is the default when OnExceeded is not set.
	BudgetStopRun BudgetExceededAction = "stop_run"

	// BudgetWaitForApproval pauses the run and waits for the caller to approve or deny
	// continuation. On Run, the registered ApprovalHandler is called. On Stream, a CUSTOM
	// AG-UI event is emitted and the caller calls Approve on the stream handle.
	//
	// For Run: WithApprovalHandler must be set when calling Run() (not at NewAgent).
	// For Stream: no ApprovalHandler is needed; set up handling via AgentStream.Approve.
	//
	// On deny the run stops with ErrBudgetExceeded. On approve the run continues; totals
	// are not reset. The next pause fires when usage grows by ApprovalExtraTokens or
	// ApprovalExtraCostUSD from the amount at approval (those default to MaxTokens /
	// MaxCostUSD when unset). After MaxApprovals approvals the next breach stops the run
	// immediately regardless of OnExceeded (defaults to 5 when unset).
	BudgetWaitForApproval BudgetExceededAction = "wait_for_approval"
)

// IsValid reports whether the action is a recognised BudgetExceededAction value.
func (a BudgetExceededAction) IsValid() bool {
	switch a {
	case BudgetStopRun, BudgetWaitForApproval:
		return true
	default:
		return false
	}
}

// BudgetExceededKind describes which limit was breached.
type BudgetExceededKind string

const (
	BudgetExceededKindTokens BudgetExceededKind = "tokens"
	BudgetExceededKindCost   BudgetExceededKind = "cost_usd"
)

// BudgetConfig configures per-run token and cost limits for an agent.
// At least one of MaxTokens or MaxCostUSD must be set.
// Limits apply to the current run only and reset when a new run starts.
//
// When the agent runs nested sub-agents, their token and cost usage is included in the
// parent run's totals. The parent budget governs the entire run tree; a budget configured
// on a sub-agent is silently ignored when the sub-agent is called from a parent.
//
// When both MaxTokens and MaxCostUSD are set, the token limit is checked first on each LLM
// call. Whichever limit is breached first triggers OnExceeded.
type BudgetConfig struct {
	// MaxTokens is the maximum total tokens (prompt + completion) allowed for a single run.
	// Zero means no token limit. Must be >= 0.
	MaxTokens int64

	// MaxCostUSD is the maximum estimated cost in US dollars allowed for a single run.
	// Zero means no cost limit. Must be >= 0.
	// Requires PromptUSDPer1M and CompletionUSDPer1M to be set when non-zero.
	MaxCostUSD float64

	// PromptUSDPer1M is the cost per one million prompt (input) tokens in US dollars.
	// Required when MaxCostUSD is non-zero. Example: 3.0 for $3 per 1M tokens. Must be >= 0.
	PromptUSDPer1M float64

	// CompletionUSDPer1M is the cost per one million completion (output) tokens in US dollars.
	// Required when MaxCostUSD is non-zero. Example: 15.0 for $15 per 1M tokens. Must be >= 0.
	CompletionUSDPer1M float64

	// OnExceeded specifies the action to take when MaxTokens or MaxCostUSD is reached.
	// Defaults to BudgetStopRun when not set.
	OnExceeded BudgetExceededAction

	// ApprovalExtraTokens is how many additional tokens are allowed after each
	// BudgetWaitForApproval approval, measured from the totals at the time of approval.
	// Zero means "use MaxTokens"; NewAgent fills that in during validation when OnExceeded is
	// BudgetWaitForApproval. Ignored when OnExceeded is BudgetStopRun. Must be >= 0.
	ApprovalExtraTokens int64

	// ApprovalExtraCostUSD is how much additional estimated USD cost is allowed after each
	// BudgetWaitForApproval approval. Zero means "use MaxCostUSD"; NewAgent fills that in
	// during validation when OnExceeded is BudgetWaitForApproval. Ignored when OnExceeded
	// is BudgetStopRun. Must be >= 0.
	ApprovalExtraCostUSD float64

	// MaxApprovals is the maximum number of times a BudgetWaitForApproval pause may be
	// approved in a single run. Once this count is reached the next budget breach stops the
	// run immediately with ErrBudgetExceeded regardless of OnExceeded.
	// Zero means "use the default of 5". Ignored when OnExceeded is BudgetStopRun.
	MaxApprovals int
}
