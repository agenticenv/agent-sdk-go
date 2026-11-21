package local

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/stretchr/testify/require"
)

func TestAgentLoop_PerRunSelectorPinsNamedAndPrimary(t *testing.T) {
	primary := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{Content: "from primary"},
		{Content: "primary again"},
	}}
	local := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "echo"}}},
		{Content: "from local"},
	}}
	var calls atomic.Int32
	rt := newSelectRT(t, primary, map[string]interfaces.LLMClient{"local": local}, func(_ context.Context, info sdkruntime.LLMSelectInfo) (string, error) {
		calls.Add(1)
		if info.Request.Messages[len(info.Request.Messages)-1].Content == "secret" {
			return "local", nil
		}
		return "", nil
	}, sdkruntime.LLMSelectPerRun)

	secret, err := runLoop(context.Background(), rt, []interfaces.Tool{stubTool{name: "echo", result: "ok"}}, AgentLoopInput{
		UserPrompt: "secret",
		RunID:      "run-secret",
	})
	require.NoError(t, err)
	require.Equal(t, "from local", secret.Content)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, 2, local.call)
	require.Equal(t, int64(2), secret.Telemetry.Run.LLMCallsByClient["local"])

	plain, err := runLoop(context.Background(), rt, nil, AgentLoopInput{UserPrompt: "hello", RunID: "run-plain"})
	require.NoError(t, err)
	require.Equal(t, "from primary", plain.Content)
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, int64(1), plain.Telemetry.Run.LLMCallsByClient[""])
}

func TestAgentLoop_PerCallSwitchesAndFinalCall(t *testing.T) {
	primary := &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "final from primary"}}}
	cheap := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "echo"}}},
	}}
	var finals []bool
	rt := newSelectRT(t, primary, map[string]interfaces.LLMClient{"cheap": cheap}, func(_ context.Context, info sdkruntime.LLMSelectInfo) (string, error) {
		finals = append(finals, info.FinalCall)
		if info.FinalCall {
			return "", nil
		}
		return "cheap", nil
	}, sdkruntime.LLMSelectPerCall)
	rt.AgentConfig.Limits.MaxIterations = 1

	result, err := runLoop(context.Background(), rt, []interfaces.Tool{stubTool{name: "echo", result: "ok"}}, AgentLoopInput{
		UserPrompt: "go",
		RunID:      "run-final",
	})
	require.NoError(t, err)
	require.Equal(t, "final from primary", result.Content)
	require.Equal(t, []bool{false, true}, finals)
	require.Equal(t, int64(1), result.Telemetry.Run.LLMCallsByClient["cheap"])
	require.Equal(t, int64(1), result.Telemetry.Run.LLMCallsByClient[""])
}

func TestAgentLoop_FallbackOverridesSelection(t *testing.T) {
	primary := &seqLLMClient{}
	local := &seqLLMClient{errs: []error{errors.New("local down")}}
	cheap := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "echo"}}},
		{Content: "from fallback"},
	}}
	var calls atomic.Int32
	rt := newSelectRT(t, primary, map[string]interfaces.LLMClient{"local": local, "cheap": cheap}, func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
		calls.Add(1)
		return "local", nil
	}, sdkruntime.LLMSelectPerRun)
	rt.AgentConfig.ErrorControl = &types.ErrorControlConfig{
		FallbackLLMClient: "cheap",
		Hooks: types.AgentErrorHooks{
			OnLLMFailure: func(context.Context, types.LLMFailureInfo) types.ErrorControlDecision {
				return types.ErrorControlDecision{Action: types.ErrorControlFallbackModel}
			},
		},
	}
	rt.AgentConfig.ExecutionConfigs.LLM.MaxAttempts = 1

	result, err := runLoop(context.Background(), rt, []interfaces.Tool{stubTool{name: "echo", result: "ok"}}, AgentLoopInput{
		UserPrompt: "go",
		RunID:      "run-fb",
	})
	require.NoError(t, err)
	require.Equal(t, "from fallback", result.Content)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, int64(2), result.Telemetry.Run.LLMCallsByClient["cheap"])
}

func TestAgentLoop_SelectorErrorDoesNotFallback(t *testing.T) {
	cheap := &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "should not run"}}}
	rt := newSelectRT(t, &seqLLMClient{}, map[string]interfaces.LLMClient{"cheap": cheap}, func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
		return "", errors.New("refuse")
	}, sdkruntime.LLMSelectPerRun)
	rt.AgentConfig.ErrorControl = &types.ErrorControlConfig{
		FallbackLLMClient: "cheap",
		Hooks: types.AgentErrorHooks{
			OnLLMFailure: func(context.Context, types.LLMFailureInfo) types.ErrorControlDecision {
				return types.ErrorControlDecision{Action: types.ErrorControlFallbackModel}
			},
		},
	}

	_, err := runLoop(context.Background(), rt, nil, AgentLoopInput{UserPrompt: "secret", RunID: "run-abort"})
	require.Error(t, err)
	require.ErrorContains(t, err, "llm select: refuse")
	require.True(t, base.IsLLMSelectAbort(err))
	require.Equal(t, 0, cheap.call)
}

func TestAgentLoop_PerRunOptionSkipsSelector(t *testing.T) {
	local := &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "override"}}}
	var calls atomic.Int32
	rt := newSelectRT(t, &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "primary"}}}, map[string]interfaces.LLMClient{"local": local}, func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
		calls.Add(1)
		return "", nil
	}, sdkruntime.LLMSelectPerCall)

	result, err := runLoop(context.Background(), rt, nil, AgentLoopInput{
		UserPrompt: "hi",
		RunID:      "run-override",
		LLMClient:  "local",
	})
	require.NoError(t, err)
	require.Equal(t, "override", result.Content)
	require.Equal(t, int32(0), calls.Load())
	require.Equal(t, int64(1), result.Telemetry.Run.LLMCallsByClient["local"])
}

func TestDurable_SelectorPinIsJournaled(t *testing.T) {
	local := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "echo"}}},
		{Content: "from local"},
	}}
	var calls atomic.Int32
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "select-journal", SystemPrompt: "sys"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:             sdkruntime.AgentLLM{Client: &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "primary"}}}},
			NamedLLMClients: map[string]interfaces.LLMClient{"local": local},
			Limits:          sdkruntime.AgentLimits{MaxIterations: 4, Timeout: 30 * time.Second},
			LLMSelector: &sdkruntime.LLMSelectorConfig{
				Mode: sdkruntime.LLMSelectPerRun,
				Select: func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
					calls.Add(1)
					return "local", nil
				},
			},
		}),
		WithLocalConfig(&LocalConfig{DataDir: t.TempDir()}),
	)
	require.NoError(t, err)
	t.Cleanup(rt.Close)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{
		UserPrompt: "go",
		Tools:      []interfaces.Tool{stubTool{name: "echo", result: "ok"}},
	})
	require.NoError(t, err)
	result, err := handle.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "from local", result.Content)
	require.Equal(t, int32(1), calls.Load())

	rec, ok, err := rt.engine.GetStep(context.Background(), rt.taskID, handle.ID(), "llm-0")
	require.NoError(t, err)
	require.True(t, ok)
	var journaled base.LLMResult
	require.NoError(t, json.Unmarshal(rec.Result, &journaled))
	require.Equal(t, "local", journaled.LLMClientName)
}

func newSelectRT(t *testing.T, primary interfaces.LLMClient, named map[string]interfaces.LLMClient, sel sdkruntime.LLMSelector, mode sdkruntime.LLMSelectMode) *LocalRuntime {
	t.Helper()
	rt, err := NewLocalRuntime(
		testNoDurability(),
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "select-agent", SystemPrompt: "sys"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:             sdkruntime.AgentLLM{Client: primary},
			NamedLLMClients: named,
			Limits:          sdkruntime.AgentLimits{MaxIterations: 4, Timeout: 10 * time.Second},
			LLMSelector:     &sdkruntime.LLMSelectorConfig{Select: sel, Mode: mode},
		}),
	)
	require.NoError(t, err)
	return rt
}
