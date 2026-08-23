package base

import (
	"fmt"
	"math"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

const (
	// nanoUSDPerUSD is the number of nano-dollars per US dollar (1e9).
	// Cost is accumulated as int64 nano-dollars to avoid float64 drift over many LLM calls.
	nanoUSDPerUSD = 1_000_000_000

	// defaultMaxApprovals is the default cap on BudgetWaitForApproval pauses per run.
	defaultMaxApprovals = 5
)

// BudgetTracker accumulates token and cost usage for one run and checks limits.
// It is not safe for concurrent use; callers must serialize access.
//
// Cost is stored internally as integer nano-dollars (1e-9 USD) to avoid floating-point
// accumulation drift over hundreds of LLM calls. Public methods return and accept float64 USD
// for API compatibility; conversions are performed at the boundary.
type BudgetTracker struct {
	cfg *types.BudgetConfig

	totalTokens      int64
	totalCostNanoUSD int64 // accumulated cost in nano-dollars

	// Precomputed integer rates (nano-USD per token) derived from the config float64 rates.
	// Computed once in NewBudgetTracker; used for accumulation instead of float64 arithmetic.
	promptRateNanoUSDPerToken     int64
	completionRateNanoUSDPerToken int64
	maxCostNanoUSD                int64 // MaxCostUSD converted to nano-dollars

	// watermark records the totals at the last approval boundary. After a
	// BudgetWaitForApproval approval, it advances to the current totals so the next
	// trigger fires only when usage grows past that point.
	watermarkTokens      int64
	watermarkCostNanoUSD int64

	// approvalCount tracks how many BudgetWaitForApproval approvals have been granted in
	// this run. When it reaches effectiveMaxApprovals, the next breach is treated as
	// BudgetStopRun regardless of cfg.OnExceeded.
	approvalCount        int
	effectiveMaxApproval int

	// hasAdded guards against RestoreState being called after the first Add, which would
	// silently corrupt accumulated totals.
	hasAdded bool
}

// NewBudgetTracker returns a tracker for cfg. Returns nil when cfg is nil (no enforcement).
// Precomputes integer nano-dollar rates from the config float64 rates to avoid accumulation drift.
func NewBudgetTracker(cfg *types.BudgetConfig) *BudgetTracker {
	if cfg == nil {
		return nil
	}
	t := &BudgetTracker{cfg: cfg}
	// Precompute: nano-USD per token = (USD / 1M tokens) * 1e9 / 1e6 = (USD/1M) * 1000.
	if cfg.PromptUSDPer1M > 0 {
		t.promptRateNanoUSDPerToken = int64(math.Round(cfg.PromptUSDPer1M * 1000))
	}
	if cfg.CompletionUSDPer1M > 0 {
		t.completionRateNanoUSDPerToken = int64(math.Round(cfg.CompletionUSDPer1M * 1000))
	}
	if cfg.MaxCostUSD > 0 {
		t.maxCostNanoUSD = int64(math.Round(cfg.MaxCostUSD * nanoUSDPerUSD))
	}
	maxApprovals := cfg.MaxApprovals
	if maxApprovals <= 0 {
		maxApprovals = defaultMaxApprovals
	}
	t.effectiveMaxApproval = maxApprovals
	return t
}

// BudgetExceededError is returned by Add or Check when a limit is breached.
// When Kind is BudgetExceededKindTokens: TotalTokens, LimitTokens, WatermarkTokens are set.
// When Kind is BudgetExceededKindCost: TotalCostUSD, LimitCostUSD, WatermarkCostUSD are set.
type BudgetExceededError struct {
	Kind             types.BudgetExceededKind
	TotalTokens      int64
	TotalCostUSD     float64
	LimitTokens      int64   // effective window size (ApprovalExtraTokens or MaxTokens)
	LimitCostUSD     float64 // effective window size in USD
	WatermarkTokens  int64   // token total at last approval (0 if never approved)
	WatermarkCostUSD float64 // cost total at last approval (0 if never approved)
}

func (e *BudgetExceededError) Error() string {
	switch e.Kind {
	case types.BudgetExceededKindCost:
		return fmt.Sprintf(
			"per-run budget exceeded: estimated cost $%.6f exceeds limit $%.6f (watermark $%.6f)",
			e.TotalCostUSD, e.LimitCostUSD, e.WatermarkCostUSD,
		)
	default:
		return fmt.Sprintf(
			"per-run budget exceeded: total tokens %d exceeds limit %d (watermark %d)",
			e.TotalTokens, e.LimitTokens, e.WatermarkTokens,
		)
	}
}

// Add accumulates usage from u and returns a BudgetExceededError if any limit is now exceeded.
// Returns nil when no limit is breached. The tracker is always updated even on error.
// u may be nil (no-op). Safe to call after a breach — subsequent calls continue accumulating.
func (t *BudgetTracker) Add(u *interfaces.LLMUsage) error {
	if t == nil || u == nil {
		return nil
	}
	t.hasAdded = true
	t.totalTokens += u.TotalTokens
	t.totalCostNanoUSD += u.PromptTokens*t.promptRateNanoUSDPerToken +
		u.CompletionTokens*t.completionRateNanoUSDPerToken
	return t.checkLimits()
}

// Check returns a BudgetExceededError when current totals breach limits relative to the
// watermark, without adding usage. Used after nested sub-agent runs that accumulated
// into a shared tracker.
func (t *BudgetTracker) Check() error {
	if t == nil {
		return nil
	}
	return t.checkLimits()
}

func (t *BudgetTracker) checkLimits() error {
	if t.cfg.MaxTokens > 0 {
		// ApprovalExtraTokens is set by validateBudget for wait_for_approval (defaults to
		// MaxTokens) and cleared for stop_run, so Extra==0 always means "use MaxTokens".
		limit := t.cfg.ApprovalExtraTokens
		if limit == 0 {
			limit = t.cfg.MaxTokens
		}
		if t.totalTokens >= t.watermarkTokens+limit {
			return &BudgetExceededError{
				Kind:            types.BudgetExceededKindTokens,
				TotalTokens:     t.totalTokens,
				LimitTokens:     limit,
				WatermarkTokens: t.watermarkTokens,
			}
		}
	}
	if t.cfg.MaxCostUSD > 0 {
		limit := t.cfg.ApprovalExtraCostUSD
		limitNano := int64(math.Round(limit * nanoUSDPerUSD))
		if limitNano == 0 {
			limitNano = t.maxCostNanoUSD
			limit = t.cfg.MaxCostUSD
		}
		if t.totalCostNanoUSD >= t.watermarkCostNanoUSD+limitNano {
			return &BudgetExceededError{
				Kind:             types.BudgetExceededKindCost,
				TotalCostUSD:     float64(t.totalCostNanoUSD) / nanoUSDPerUSD,
				LimitCostUSD:     limit,
				WatermarkCostUSD: float64(t.watermarkCostNanoUSD) / nanoUSDPerUSD,
			}
		}
	}
	return nil
}

// AdvanceWatermark moves the approval watermark to the current totals after a
// BudgetWaitForApproval approval and increments the approval counter.
// Returns false when MaxApprovals has been reached — caller must treat the next breach as
// BudgetStopRun.
func (t *BudgetTracker) AdvanceWatermark() (approvalGranted bool) {
	if t == nil {
		return true
	}
	t.approvalCount++
	t.watermarkTokens = t.totalTokens
	t.watermarkCostNanoUSD = t.totalCostNanoUSD
	return true
}

// ApprovalsExhausted reports whether the run has consumed all allowed approval pauses.
// When true, the next budget breach must be treated as BudgetStopRun.
func (t *BudgetTracker) ApprovalsExhausted() bool {
	if t == nil {
		return false
	}
	return t.approvalCount >= t.effectiveMaxApproval
}

// ApprovalCount returns how many BudgetWaitForApproval approvals have been granted this run.
func (t *BudgetTracker) ApprovalCount() int {
	if t == nil {
		return 0
	}
	return t.approvalCount
}

// Totals returns the accumulated token count and estimated cost in US dollars for the run.
func (t *BudgetTracker) Totals() (tokens int64, costUSD float64) {
	if t == nil {
		return 0, 0
	}
	return t.totalTokens, float64(t.totalCostNanoUSD) / nanoUSDPerUSD
}

// WatermarkTotals returns the token and USD values at the last approval watermark.
func (t *BudgetTracker) WatermarkTotals() (tokens int64, costUSD float64) {
	if t == nil {
		return 0, 0
	}
	return t.watermarkTokens, float64(t.watermarkCostNanoUSD) / nanoUSDPerUSD
}

// RestoreState reloads accumulated totals and watermark values from durable state (e.g. after
// a Temporal ContinueAsNew). Must be called before any Add; panics otherwise to prevent
// silent corruption of accumulated totals.
func (t *BudgetTracker) RestoreState(tokens int64, costUSD float64, wmTokens int64, wmCostUSD float64) {
	if t == nil {
		return
	}
	if t.hasAdded {
		panic("budget: RestoreState called after Add — restore must happen before accumulation begins")
	}
	t.totalTokens = tokens
	t.totalCostNanoUSD = int64(math.Round(costUSD * nanoUSDPerUSD))
	t.watermarkTokens = wmTokens
	t.watermarkCostNanoUSD = int64(math.Round(wmCostUSD * nanoUSDPerUSD))
}
