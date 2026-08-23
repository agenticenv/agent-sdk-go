package temporal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestAgentWorkflow_BudgetStopRun(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}
	rt.AgentConfig.Limits.ApprovalTimeout = 5 * time.Second

	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(
		&AgentLLMResult{
			Content: "too much",
			Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
		}, nil)

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{UserPrompt: "hello"})
	require.True(t, env.IsWorkflowCompleted())
	wfErr := env.GetWorkflowError()
	require.Error(t, wfErr)
	require.True(t,
		errors.Is(wfErr, types.ErrBudgetExceeded) || strings.Contains(wfErr.Error(), "budget"),
		"expected budget exceeded, got: %v", wfErr)
}

func TestAgentWorkflow_BudgetWaitForApproval_Approved(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	rt.AgentConfig.Limits.ApprovalTimeout = 5 * time.Second

	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(
		&AgentLLMResult{
			Content: "first",
			Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
		}, nil)
	env.OnActivity(rt.AgentBudgetApprovalActivity, mock.Anything, mock.Anything).Return(
		types.ApprovalStatusApproved, nil)

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{UserPrompt: "hello"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result types.AgentRunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "first", result.Content)
}

func TestAgentWorkflow_BudgetWaitForApproval_Denied(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	rt.AgentConfig.Limits.ApprovalTimeout = 5 * time.Second

	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(
		&AgentLLMResult{
			Content: "first",
			Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
		}, nil)
	env.OnActivity(rt.AgentBudgetApprovalActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, in AgentBudgetApprovalInput) (types.ApprovalStatus, error) {
			require.Equal(t, int64(110), in.TotalTokens)
			return types.ApprovalStatusRejected, nil
		})

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{UserPrompt: "hello"})
	require.True(t, env.IsWorkflowCompleted())
	wfErr := env.GetWorkflowError()
	require.Error(t, wfErr)
	require.True(t,
		errors.Is(wfErr, types.ErrBudgetExceeded) || strings.Contains(wfErr.Error(), "budget"),
		"expected budget exceeded, got: %v", wfErr)
}

func TestAgentWorkflow_BudgetStopRun_MaxCostUSD(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxCostUSD:         0.001,
		PromptUSDPer1M:     1.0,
		CompletionUSDPer1M: 3.0,
		OnExceeded:         types.BudgetStopRun,
	}

	env.RegisterWorkflow(rt.AgentWorkflow)
	// 500 prompt + 200 completion = 0.0005 + 0.0006 = 0.0011 USD
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(
		&AgentLLMResult{
			Content: "too costly",
			Usage:   &interfaces.LLMUsage{PromptTokens: 500, CompletionTokens: 200, TotalTokens: 700},
		}, nil)

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{UserPrompt: "hello"})
	require.True(t, env.IsWorkflowCompleted())
	wfErr := env.GetWorkflowError()
	require.Error(t, wfErr)
	require.True(t, strings.Contains(wfErr.Error(), "budget"), "got: %v", wfErr)
}

func TestAgentWorkflow_BudgetRestoredFromContinueAsNewState(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}

	env.RegisterWorkflow(rt.AgentWorkflow)
	// Prior CAN left 90 tokens; this LLM adds 20 → 110 → exceed.
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(
		&AgentLLMResult{
			Content: "over",
			Usage:   &interfaces.LLMUsage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
		}, nil)

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{
		UserPrompt: "hello",
		State: &AgentWorkflowState{
			Iteration:    0,
			Messages:     []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "hello"}},
			BudgetTokens: 90,
		},
	})
	require.True(t, env.IsWorkflowCompleted())
	wfErr := env.GetWorkflowError()
	require.Error(t, wfErr)
	require.True(t, strings.Contains(wfErr.Error(), "budget"), "got: %v", wfErr)
}

func TestAgentWorkflow_BudgetWaitTwice_UniqueActivityIDs(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:           100,
		ApprovalExtraTokens: 100,
		OnExceeded:          types.BudgetWaitForApproval,
	}
	rt.AgentConfig.Limits.ApprovalTimeout = 5 * time.Second
	rt.ToolExecutionMode = types.AgentToolExecutionModeSequential

	var activityIDs []string
	var llmCalls int
	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentLLMInput) (*AgentLLMResult, error) {
		llmCalls++
		switch llmCalls {
		case 1:
			return &AgentLLMResult{
				Content:   "need tool",
				ToolCalls: []ToolCallRequest{testWorkflowToolCall("tc1", "echo", types.ToolKindNative, map[string]any{"x": 1})},
				Usage:     &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
			}, nil
		case 2:
			return &AgentLLMResult{
				Content: "final",
				Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
			}, nil
		default:
			return &AgentLLMResult{Content: "extra"}, nil
		}
	})
	env.OnActivity(rt.AgentToolAuthorizeActivity, mock.Anything, mock.Anything).Return(AgentToolAuthorizeResult{Allowed: true}, nil)
	env.OnActivity(rt.AgentToolExecuteActivity, mock.Anything, mock.Anything).Return("ok", nil)
	env.OnActivity(rt.AgentBudgetApprovalActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, in AgentBudgetApprovalInput) (types.ApprovalStatus, error) {
			info := activity.GetInfo(ctx)
			activityIDs = append(activityIDs, info.ActivityID)
			return types.ApprovalStatusApproved, nil
		})

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{UserPrompt: "hello"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Len(t, activityIDs, 2)
	require.NotEqual(t, activityIDs[0], activityIDs[1], "second budget pause must use a distinct ActivityID")
	var result types.AgentRunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "final", result.Content)
}

func TestAgentWorkflow_BudgetAfterNestedSubagent(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}
	rt.ToolExecutionMode = types.AgentToolExecutionModeSequential

	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(
		&AgentLLMResult{
			Content:   "delegate",
			ToolCalls: []ToolCallRequest{testWorkflowToolCall("tc1", "research", types.ToolKindSubAgent, map[string]any{"query": "q"})},
			Usage:     &interfaces.LLMUsage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
		}, nil)
	env.OnActivity(rt.AgentToolAuthorizeActivity, mock.Anything, mock.Anything).Return(AgentToolAuthorizeResult{Allowed: true}, nil)
	// OnWorkflow stubs every AgentWorkflow invocation (including the root). Run the real
	// workflow for the root; return canned child usage for nested RootWorkflowID != "".
	env.OnWorkflow(rt.AgentWorkflow, mock.Anything, mock.Anything).Return(
		func(ctx workflow.Context, in AgentWorkflowInput) (*types.AgentRunResult, error) {
			if in.RootWorkflowID != "" {
				return &types.AgentRunResult{
					Content:  "child done",
					LLMUsage: &interfaces.LLMUsage{PromptTokens: 50, CompletionTokens: 50, TotalTokens: 100},
				}, nil
			}
			return rt.AgentWorkflow(ctx, in)
		})

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{
		UserPrompt:       "go",
		MaxSubAgentDepth: 2,
		SubAgentRoutes: map[string]SubAgentRoute{
			"research": {Name: "researcher", TaskQueue: "child-tq"},
		},
	})
	require.True(t, env.IsWorkflowCompleted())
	wfErr := env.GetWorkflowError()
	require.Error(t, wfErr)
	require.True(t, strings.Contains(wfErr.Error(), "budget"), "got: %v", wfErr)
}
