package restate

import (
	"errors"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/x/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestExecuteAgentLoop_BudgetStopRun(t *testing.T) {
	rt := testRestateRuntime("budget-stop")
	rt.AgentConfig.Limits.MaxIterations = 3
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{
		Content: "too much",
		Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
	}}}

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once() // message-id
	expectRunExecutes(ctx, 6).Once() // llm

	result, err := rt.executeAgentLoop(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{RunID: "run-1", UserPrompt: "hi"},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, types.ErrBudgetExceeded), "got: %v", err)
	require.NotNil(t, result)
	require.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

func expectBudgetApprovalAwakeable(t *testing.T, ctx *mocks.MockContext, status types.ApprovalStatus) {
	t.Helper()
	awake := mocks.NewMockAwakeableFuture(t)
	awake.EXPECT().Id().Return("budget-tok")
	awake.EXPECT().ResultAndReturn(status, nil).Once()
	ctx.EXPECT().Awakeable().Return(awake).Once()

	after := ctx.EXPECT().MockAfter(mock.Anything)
	wait := ctx.EXPECT().MockWaitIter(mock.Anything, mock.Anything)
	wait.Next().Return(true).Once()
	wait.Value().Return(awake).Once()
	wait.Err().Return(nil).Once()
	_ = after
}

func TestExecuteAgentLoop_BudgetWaitForApproval_Approved(t *testing.T) {
	rt := testRestateRuntime("budget-wait-ok")
	rt.AgentConfig.Limits.MaxIterations = 3
	rt.AgentConfig.Limits.ApprovalTimeout = 5 * time.Second
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{
		Content: "first",
		Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
	}}}

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()
	expectBudgetApprovalAwakeable(t, ctx, types.ApprovalStatusApproved)

	result, err := rt.executeAgentLoop(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{RunID: "run-1", UserPrompt: "hi"},
	})
	require.NoError(t, err)
	require.Equal(t, "first", result.Content)
}

func TestExecuteAgentLoop_BudgetWaitForApproval_Denied(t *testing.T) {
	rt := testRestateRuntime("budget-wait-deny")
	rt.AgentConfig.Limits.MaxIterations = 3
	rt.AgentConfig.Limits.ApprovalTimeout = 5 * time.Second
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{
		Content: "first",
		Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
	}}}

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()
	expectBudgetApprovalAwakeable(t, ctx, types.ApprovalStatusRejected)

	result, err := rt.executeAgentLoop(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{RunID: "run-1", UserPrompt: "hi"},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, types.ErrBudgetExceeded), "got: %v", err)
	require.NotNil(t, result)
	require.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

func TestExecuteAgentLoop_BudgetStopRun_MaxCostUSD(t *testing.T) {
	rt := testRestateRuntime("budget-cost")
	rt.AgentConfig.Limits.MaxIterations = 3
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxCostUSD:         0.001,
		PromptUSDPer1M:     1.0,
		CompletionUSDPer1M: 3.0,
		OnExceeded:         types.BudgetStopRun,
	}
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{
		Content: "too costly",
		Usage:   &interfaces.LLMUsage{PromptTokens: 500, CompletionTokens: 200, TotalTokens: 700},
	}}}

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()

	result, err := rt.executeAgentLoop(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{RunID: "run-1", UserPrompt: "hi"},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, types.ErrBudgetExceeded), "got: %v", err)
	require.NotNil(t, result)
	require.Equal(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}

func TestExecuteAgentLoop_BudgetNestedChildDoesNotEnforce(t *testing.T) {
	// Nested Restate runs accumulate usage but do not apply OnExceeded (enforceBudget is false
	// when SubAgentDepth > 0). Parent merges child usage after tools.
	rt := testRestateRuntime("budget-nested")
	rt.AgentConfig.Limits.MaxIterations = 3
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{
		Content: "child",
		Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
	}}}

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()

	result, err := rt.executeAgentLoop(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{RunID: "run-child", UserPrompt: "hi", SubAgentDepth: 1},
	})
	require.NoError(t, err)
	require.Equal(t, "child", result.Content)
	require.NotEqual(t, types.FinishReasonBudgetExceeded, result.Telemetry.Run.FinishReason)
}
