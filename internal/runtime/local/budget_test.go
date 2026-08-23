package local

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// llmClientWithUsage wraps a seqLLMClient and injects LLMUsage into responses.
type llmClientWithUsage struct {
	mu        sync.Mutex
	responses []*interfaces.LLMResponse
	call      int
}

func (s *llmClientWithUsage) Generate(_ context.Context, _ *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.call
	s.call++
	if i < len(s.responses) {
		return s.responses[i], nil
	}
	return &interfaces.LLMResponse{Content: "done"}, nil
}
func (s *llmClientWithUsage) GenerateStream(_ context.Context, _ *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stream not implemented")
}
func (s *llmClientWithUsage) GetModel() string { return "budget-test-model" }
func (s *llmClientWithUsage) GetProvider() interfaces.LLMProvider {
	return interfaces.LLMProviderOpenAI
}
func (s *llmClientWithUsage) IsStreamSupported() bool { return false }

func llmResp(content string, promptTokens, completionTokens, totalTokens int64) *interfaces.LLMResponse {
	return &interfaces.LLMResponse{
		Content: content,
		Usage: &interfaces.LLMUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
		},
	}
}

func newBudgetRT(t *testing.T, budget *types.BudgetConfig, client interfaces.LLMClient, approvalHandler types.ApprovalHandler) *LocalRuntime {
	t.Helper()
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "budget-agent", SystemPrompt: "sys"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: client},
			Limits: sdkruntime.AgentLimits{
				MaxIterations:   10,
				Timeout:         10 * time.Second,
				ApprovalTimeout: 5 * time.Second,
				Budget:          budget,
			},
		}),
		WithApprovalHandler(approvalHandler),
	)
	require.NoError(t, err)
	return rt
}

// TestBudgetEnforcement_StopRun verifies that when MaxTokens is exceeded and
// OnExceeded is BudgetStopRun, the loop returns ErrBudgetExceeded and sets
// FinishReasonBudgetExceeded.
func TestBudgetEnforcement_StopRun(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("answer", 60, 50, 110), // 110 tokens → exceeds MaxTokens=100
		},
	}
	budget := &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}
	rt := newBudgetRT(t, budget, client, nil)
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:    "hello",
		BudgetTracker: tracker,
		EnforceBudget: true,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded), "expected ErrBudgetExceeded, got: %v", err)
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

// TestBudgetEnforcement_NoBudget verifies that when no budget is configured
// the loop completes normally even with high token usage.
func TestBudgetEnforcement_NoBudget(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("answer", 1000, 2000, 3000),
		},
	}
	rt := newBudgetRT(t, nil, client, nil)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:    "hello",
		BudgetTracker: nil,
		EnforceBudget: false,
	})
	require.NoError(t, err)
	assert.Equal(t, "answer", result.Content)
}

// TestBudgetEnforcement_WaitForApproval_Approved verifies that when BudgetWaitForApproval
// is configured and the approval handler approves, the run completes (not stopped with error).
// The LLM returned a final answer (no tool calls) during the breaching iteration, so "first"
// is returned after approval because the loop already had its final answer.
func TestBudgetEnforcement_WaitForApproval_Approved(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("first", 60, 50, 110), // exceeds 100 → approval requested; no tool calls → final answer
		},
	}
	budget := &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	approvalHandler := func(_ context.Context, req *types.ApprovalRequest) {
		_ = req.Respond(types.ApprovalStatusApproved)
	}
	rt := newBudgetRT(t, budget, client, approvalHandler)
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:      "hello",
		BudgetTracker:   tracker,
		EnforceBudget:   true,
		ApprovalHandler: approvalHandler,
	})
	require.NoError(t, err)
	assert.Equal(t, "first", result.Content)
}

// TestBudgetEnforcement_WaitForApproval_Denied verifies that when BudgetWaitForApproval
// is configured and the approval handler denies, the run stops with ErrBudgetExceeded.
func TestBudgetEnforcement_WaitForApproval_Denied(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("first", 60, 50, 110), // exceeds 100 → approval requested
		},
	}
	budget := &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	approvalHandler := func(_ context.Context, req *types.ApprovalRequest) {
		_ = req.Respond(types.ApprovalStatusRejected)
	}
	rt := newBudgetRT(t, budget, client, approvalHandler)
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:      "hello",
		BudgetTracker:   tracker,
		EnforceBudget:   true,
		ApprovalHandler: approvalHandler,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded))
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

// TestBudgetEnforcement_SubagentAccumulatesUnderLimit runs a real nested sub-agent that
// stays under the shared parent budget; usage is accumulated and the parent continues.
func TestBudgetEnforcement_SubagentAccumulatesUnderLimit(t *testing.T) {
	budget := &types.BudgetConfig{
		MaxTokens:  1000,
		OnExceeded: types.BudgetStopRun,
	}
	tracker := base.NewBudgetTracker(budget)

	childClient := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("child done", 50, 50, 100),
		},
	}
	childRT := newBudgetRT(t, nil, childClient, nil)

	parentClient := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			{
				ToolCalls: []*interfaces.ToolCall{{
					ToolCallID: "c1",
					ToolName:   "research",
					Args:       map[string]any{"query": "q"},
				}},
				Usage: &interfaces.LLMUsage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
			},
			llmResp("parent done", 5, 5, 10),
		},
	}
	parentRT := newBudgetRT(t, budget, parentClient, nil)
	delegateTool := stubKindTool{
		stubTool: stubTool{name: "research", result: "unused"},
		kind:     types.ToolKindSubAgent,
	}

	result, err := parentRT.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:       "go",
		Tools:            []interfaces.Tool{delegateTool},
		BudgetTracker:    tracker,
		EnforceBudget:    true,
		MaxSubAgentDepth: 2,
		SubAgentRoutes: map[string]subAgentRoute{
			"research": {name: "researcher", runtime: childRT},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "parent done", result.Content)

	tokens, _ := tracker.Totals()
	assert.Equal(t, int64(120), tokens, "parent + child LLM usage should accumulate in shared tracker")
}

// TestBudgetEnforcement_AfterNestedSubagent verifies the parent enforces immediately
// after a nested sub-agent returns (shared tracker already over limit), without waiting
// for another parent LLM call.
func TestBudgetEnforcement_AfterNestedSubagent(t *testing.T) {
	budget := &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}
	tracker := base.NewBudgetTracker(budget)

	childClient := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("child done", 50, 50, 100), // pushes shared total over 100
		},
	}
	childRT := newBudgetRT(t, nil, childClient, nil)

	parentClient := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			{
				ToolCalls: []*interfaces.ToolCall{{
					ToolCallID: "c1",
					ToolName:   "research",
					Args:       map[string]any{"query": "q"},
				}},
				Usage: &interfaces.LLMUsage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
			},
			llmResp("should-not-run", 1, 1, 2),
		},
	}
	parentRT := newBudgetRT(t, budget, parentClient, nil)
	delegateTool := stubKindTool{
		stubTool: stubTool{name: "research", result: "unused"},
		kind:     types.ToolKindSubAgent,
	}

	result, err := parentRT.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:       "go",
		Tools:            []interfaces.Tool{delegateTool},
		BudgetTracker:    tracker,
		EnforceBudget:    true,
		MaxSubAgentDepth: 2,
		SubAgentRoutes: map[string]subAgentRoute{
			"research": {name: "researcher", runtime: childRT},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded), "expected ErrBudgetExceeded, got: %v", err)
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)

	parentClient.mu.Lock()
	parentCalls := parentClient.call
	parentClient.mu.Unlock()
	assert.Equal(t, 1, parentCalls, "parent must not make another LLM call after nested budget stop")

	tokens, _ := tracker.Totals()
	assert.Equal(t, int64(110), tokens)
}

// TestBudgetEnforcement_AfterNestedSubagent_WaitForApproval verifies nested over-limit
// uses wait-for-approval on the parent after the child returns.
func TestBudgetEnforcement_AfterNestedSubagent_WaitForApproval(t *testing.T) {
	budget := &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	tracker := base.NewBudgetTracker(budget)

	childClient := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("child done", 50, 50, 100),
		},
	}
	childRT := newBudgetRT(t, nil, childClient, nil)

	parentClient := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			{
				ToolCalls: []*interfaces.ToolCall{{
					ToolCallID: "c1",
					ToolName:   "research",
					Args:       map[string]any{"query": "q"},
				}},
				Usage: &interfaces.LLMUsage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
			},
			llmResp("parent after approve", 5, 5, 10),
		},
	}
	approvalHandler := func(_ context.Context, req *types.ApprovalRequest) {
		require.Equal(t, types.ApprovalRequestNameBudget, req.Name)
		_ = req.Respond(types.ApprovalStatusApproved)
	}
	parentRT := newBudgetRT(t, budget, parentClient, approvalHandler)
	delegateTool := stubKindTool{
		stubTool: stubTool{name: "research", result: "unused"},
		kind:     types.ToolKindSubAgent,
	}

	result, err := parentRT.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:       "go",
		Tools:            []interfaces.Tool{delegateTool},
		BudgetTracker:    tracker,
		EnforceBudget:    true,
		ApprovalHandler:  approvalHandler,
		MaxSubAgentDepth: 2,
		SubAgentRoutes: map[string]subAgentRoute{
			"research": {name: "researcher", runtime: childRT},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "parent after approve", result.Content)
}

// TestBudgetEnforcement_StopRun_MaxCostUSD exercises cost-limit enforcement in the local loop.
func TestBudgetEnforcement_StopRun_MaxCostUSD(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			{
				Content: "too costly",
				Usage: &interfaces.LLMUsage{
					PromptTokens:     500,
					CompletionTokens: 200,
					TotalTokens:      700,
				},
			},
		},
	}
	budget := &types.BudgetConfig{
		MaxCostUSD:         0.001,
		PromptUSDPer1M:     1.0,
		CompletionUSDPer1M: 3.0,
		OnExceeded:         types.BudgetStopRun,
	}
	rt := newBudgetRT(t, budget, client, nil)
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:    "hello",
		BudgetTracker: tracker,
		EnforceBudget: true,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded), "got: %v", err)
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

// TestBudgetEnforcement_TokenAndCostSimultaneous verifies that when both MaxTokens and
// MaxCostUSD are set, the token limit fires first (checked before cost in checkLimits).
func TestBudgetEnforcement_TokenAndCostSimultaneous(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			// 110 tokens → exceeds MaxTokens=100 AND would exceed MaxCostUSD if checked.
			llmResp("answer", 60, 50, 110),
		},
	}
	budget := &types.BudgetConfig{
		MaxTokens:          100,
		MaxCostUSD:         0.001,
		PromptUSDPer1M:     1.0,
		CompletionUSDPer1M: 3.0,
		OnExceeded:         types.BudgetStopRun,
	}
	rt := newBudgetRT(t, budget, client, nil)
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:    "hello",
		BudgetTracker: tracker,
		EnforceBudget: true,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded))
	// Token limit fires first: the BudgetExceededError kind must be tokens.
	var budgetErr *base.BudgetExceededError
	require.ErrorAs(t, err, &budgetErr)
	assert.Equal(t, types.BudgetExceededKindTokens, budgetErr.Kind)
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

// TestBudgetEnforcement_CostLimitBeforeTokenLimit verifies cost fires when token limit is not set
// and cost is breached first.
func TestBudgetEnforcement_CostLimitBeforeTokenLimit(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("answer", 500, 200, 700),
		},
	}
	budget := &types.BudgetConfig{
		MaxCostUSD:         0.001,
		PromptUSDPer1M:     1.0,
		CompletionUSDPer1M: 3.0,
		OnExceeded:         types.BudgetStopRun,
	}
	rt := newBudgetRT(t, budget, client, nil)
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:    "hello",
		BudgetTracker: tracker,
		EnforceBudget: true,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded))
	var budgetErr *base.BudgetExceededError
	require.ErrorAs(t, err, &budgetErr)
	assert.Equal(t, types.BudgetExceededKindCost, budgetErr.Kind)
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

// TestBudgetEnforcement_ApprovalTimedOut verifies that a timed-out budget approval
// (handler never responds within timeout) stops the run with ErrBudgetExceeded.
func TestBudgetEnforcement_ApprovalTimedOut(t *testing.T) {
	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			llmResp("first", 60, 50, 110), // exceeds 100 → approval requested
		},
	}
	budget := &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	// Handler never responds — the run context will expire or the loop will timeout.
	// We simulate a timed-out response by sending ApprovalStatusTimedOut from the handler.
	approvalHandler := func(_ context.Context, req *types.ApprovalRequest) {
		_ = req.Respond(types.ApprovalStatusTimedOut)
	}
	rt := newBudgetRT(t, budget, client, approvalHandler)
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:      "hello",
		BudgetTracker:   tracker,
		EnforceBudget:   true,
		ApprovalHandler: approvalHandler,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded), "timed-out approval must stop the run: %v", err)
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

// TestBudgetEnforcement_MaxApprovalsExhausted verifies that once MaxApprovals is reached
// the next budget breach stops the run even though OnExceeded is BudgetWaitForApproval.
// The LLM first returns a tool call (so the loop iterates), approval #1 is granted, then
// on the second LLM call the total exceeds the budget again and MaxApprovals is exhausted.
func TestBudgetEnforcement_MaxApprovalsExhausted(t *testing.T) {
	// MaxApprovals=1: first breach → approval; second breach → exhausted → stop.
	budget := &types.BudgetConfig{
		MaxTokens:           50,
		ApprovalExtraTokens: 50,
		OnExceeded:          types.BudgetWaitForApproval,
		MaxApprovals:        1,
	}

	approvalCalls := 0
	approvalHandler := func(_ context.Context, req *types.ApprovalRequest) {
		approvalCalls++
		_ = req.Respond(types.ApprovalStatusApproved)
	}

	client := &llmClientWithUsage{
		responses: []*interfaces.LLMResponse{
			// First call: tool call + 60 tokens → 60 > MaxTokens(50) → approval #1 → approved.
			// Loop continues because there are tool calls.
			{
				ToolCalls: []*interfaces.ToolCall{{
					ToolCallID: "c1",
					ToolName:   "noop",
					Args:       map[string]any{},
				}},
				Usage: &interfaces.LLMUsage{PromptTokens: 30, CompletionTokens: 30, TotalTokens: 60},
			},
			// Second call: 60 more tokens → total=120 >= watermark(60)+50=110 → breach again.
			// MaxApprovals=1 already exhausted → stop with ErrBudgetExceeded.
			llmResp("second", 30, 30, 60),
		},
	}
	rt := newBudgetRT(t, budget, client, approvalHandler)
	noopTool := stubTool{name: "noop", result: "ok"}
	tracker := base.NewBudgetTracker(budget)
	result, err := rt.executeAgentLoop(context.Background(), AgentLoopInput{
		UserPrompt:      "hello",
		Tools:           []interfaces.Tool{noopTool},
		BudgetTracker:   tracker,
		EnforceBudget:   true,
		ApprovalHandler: approvalHandler,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, types.ErrBudgetExceeded), "got: %v", err)
	require.NotNil(t, result)
	assert.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
	assert.Equal(t, 1, approvalCalls, "approval handler should be called exactly once before exhaustion")
}
