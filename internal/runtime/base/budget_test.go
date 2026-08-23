package base_test

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usage(prompt, completion, total int64) *interfaces.LLMUsage {
	return &interfaces.LLMUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
	}
}

func TestNewBudgetTracker_NilConfig(t *testing.T) {
	bt := base.NewBudgetTracker(nil)
	assert.Nil(t, bt)
	// nil tracker must be safe to call
	assert.NoError(t, bt.Add(usage(100, 50, 150)))
	bt.AdvanceWatermark()
	tok, cost := bt.Totals()
	assert.Equal(t, int64(0), tok)
	assert.Equal(t, float64(0), cost)
}

func TestBudgetTracker_NoLimitExceeded(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:  1000,
		OnExceeded: types.BudgetStopRun,
	})
	require.NotNil(t, bt)
	err := bt.Add(usage(100, 50, 150))
	assert.NoError(t, err)
	tokens, _ := bt.Totals()
	assert.Equal(t, int64(150), tokens)
}

func TestBudgetTracker_TokenLimitExceeded(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	})
	err := bt.Add(usage(60, 50, 110))
	require.Error(t, err)
	var budgetErr *base.BudgetExceededError
	require.ErrorAs(t, err, &budgetErr)
	assert.Equal(t, types.BudgetExceededKindTokens, budgetErr.Kind)
	assert.Equal(t, int64(110), budgetErr.TotalTokens)
	assert.Equal(t, int64(100), budgetErr.LimitTokens)
	assert.Equal(t, int64(0), budgetErr.WatermarkTokens)
}

func TestBudgetTracker_CostLimitExceeded(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxCostUSD:         0.001,
		PromptUSDPer1M:     1.0,
		CompletionUSDPer1M: 3.0,
		OnExceeded:         types.BudgetStopRun,
	})
	// 500 prompt + 200 completion = 0.0005 + 0.0006 = 0.0011 USD
	err := bt.Add(usage(500, 200, 700))
	require.Error(t, err)
	var budgetErr *base.BudgetExceededError
	require.ErrorAs(t, err, &budgetErr)
	assert.Equal(t, types.BudgetExceededKindCost, budgetErr.Kind)
	assert.InDelta(t, 0.0011, budgetErr.TotalCostUSD, 1e-6)
	assert.Equal(t, float64(0), budgetErr.WatermarkCostUSD)
}

func TestBudgetTracker_AdvanceWatermark(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100, // same as MaxTokens after NewAgent defaulting
		OnExceeded:          types.BudgetWaitForApproval,
	})
	// First breach at 110 tokens.
	err := bt.Add(usage(60, 50, 110))
	require.Error(t, err)

	// Simulate approval: advance watermark.
	assert.True(t, bt.AdvanceWatermark())

	// After advancing watermark to 110, the next trigger is at watermark+extra = 110+100 = 210.
	// Adding 15 = total 125; still under 210 — no breach.
	err = bt.Add(usage(10, 5, 15))
	assert.NoError(t, err, "after watermark advance small usage should not re-trigger")

	// Adding 100 more brings total to 225 which exceeds 210 — new breach.
	err = bt.Add(usage(60, 50, 100))
	assert.Error(t, err, "exceeding watermark+limit should trigger again")
}

func TestBudgetTracker_ApprovalExtraTokens(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:           1000,
		ApprovalExtraTokens: 100, // smaller top-up after each approval
		OnExceeded:          types.BudgetWaitForApproval,
	})
	require.Error(t, bt.Add(usage(50, 50, 100))) // first window uses Extra=100
	assert.True(t, bt.AdvanceWatermark())

	require.NoError(t, bt.Add(usage(40, 40, 80))) // 180 < 100+100
	require.Error(t, bt.Add(usage(20, 20, 40)))   // 220 >= 200
}

func TestBudgetTracker_StopRunUsesMaxTokens(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	})
	require.NoError(t, bt.Add(usage(40, 40, 80)))
	require.Error(t, bt.Add(usage(20, 20, 30))) // 110 >= 100
}

func TestBudgetTracker_RestoreState(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:  500,
		OnExceeded: types.BudgetStopRun,
	})
	// Restore: total=400, watermark=0 (never approved). Next trigger at 0+500=500.
	bt.RestoreState(400, 0.004, 0, 0)
	tok, cost := bt.Totals()
	assert.Equal(t, int64(400), tok)
	assert.InDelta(t, 0.004, cost, 1e-10)

	// Adding 110 tokens brings total to 510, which crosses 0+500=500 limit.
	err := bt.Add(usage(60, 50, 110))
	assert.Error(t, err)
}

func TestBudgetTracker_NilUsage(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	})
	err := bt.Add(nil)
	assert.NoError(t, err)
}

func TestBudgetTracker_Check(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	})
	require.NoError(t, bt.Check())

	require.Error(t, bt.Add(usage(60, 50, 110)))
	require.Error(t, bt.Check(), "Check should report breach without adding")

	assert.True(t, bt.AdvanceWatermark())
	require.NoError(t, bt.Check(), "after watermark, current totals are within the next window")
}

func TestBudgetTracker_RestoreState_PanicsAfterAdd(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:  500,
		OnExceeded: types.BudgetStopRun,
	})
	_ = bt.Add(usage(10, 10, 20))
	assert.Panics(t, func() {
		bt.RestoreState(100, 0.001, 0, 0)
	}, "RestoreState after Add must panic")
}

func TestBudgetTracker_ApprovalsExhausted(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
		MaxApprovals:        2,
	})
	assert.False(t, bt.ApprovalsExhausted())

	bt.AdvanceWatermark()
	assert.Equal(t, 1, bt.ApprovalCount())
	assert.False(t, bt.ApprovalsExhausted())

	bt.AdvanceWatermark()
	assert.Equal(t, 2, bt.ApprovalCount())
	assert.True(t, bt.ApprovalsExhausted(), "after MaxApprovals=2 advances, must be exhausted")
}

func TestBudgetTracker_WatermarkIncludedInError(t *testing.T) {
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	})
	_ = bt.Add(usage(60, 50, 110)) // first breach; watermark=0
	bt.AdvanceWatermark()          // watermark advances to 110

	// Second breach; watermark should appear in error.
	err := bt.Add(usage(60, 50, 100))
	require.Error(t, err)
	var budgetErr *base.BudgetExceededError
	require.ErrorAs(t, err, &budgetErr)
	assert.Equal(t, int64(110), budgetErr.WatermarkTokens, "watermark should be 110 after first approval")
	assert.Equal(t, int64(210), budgetErr.TotalTokens)
}

func TestBudgetTracker_IntegerCostAccumulation_NoDrift(t *testing.T) {
	// Verify that 1000 identical LLM calls accumulate cost without float64 drift.
	bt := base.NewBudgetTracker(&types.BudgetConfig{
		MaxCostUSD:         100.0,
		PromptUSDPer1M:     3.0,
		CompletionUSDPer1M: 15.0,
		OnExceeded:         types.BudgetStopRun,
	})
	// Each call: 100 prompt + 50 completion.
	// cost = (100*3 + 50*15)/1e6 = (300+750)/1e6 = 0.00105 USD
	for i := 0; i < 1000; i++ {
		u := usage(100, 50, 150)
		_ = bt.Add(u)
	}
	_, cost := bt.Totals()
	// Expected: 1000 * 0.00105 = 1.05 USD
	assert.InDelta(t, 1.05, cost, 1e-9, "integer accumulation must not drift beyond 1 nano-dollar")
}
