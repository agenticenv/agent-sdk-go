package base

import (
	"context"
	"errors"
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/stretchr/testify/require"
)

type namedLLM struct{ model string }

func (n namedLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{Content: n.model}, nil
}
func (namedLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("no stream")
}
func (n namedLLM) GetModel() string                  { return n.model }
func (namedLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (namedLLM) IsStreamSupported() bool             { return false }

func TestDecideLLMFailure_DefaultAbort(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	d := rt.DecideLLMFailure(context.Background(), types.LLMFailureInfo{Attempt: 3, Err: errors.New("x")}, logger.NoopLogger())
	require.Equal(t, types.ErrorControlAbort, d.Action)
}

func TestDecideLLMFailure_FallbackAndInvalid(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		NamedLLMClients: map[string]interfaces.LLMClient{"cheap": namedLLM{model: "haiku"}},
		ErrorControl: &types.ErrorControlConfig{
			FallbackLLMClient: "cheap",
			Hooks: types.AgentErrorHooks{
				OnLLMFailure: func(context.Context, types.LLMFailureInfo) types.ErrorControlDecision {
					return types.ErrorControlDecision{Action: types.ErrorControlFallbackModel}
				},
			},
		},
	})
	d := rt.DecideLLMFailure(context.Background(), types.LLMFailureInfo{Err: errors.New("rate")}, logger.NoopLogger())
	require.Equal(t, types.ErrorControlFallbackModel, d.Action)

	rt.AgentConfig.ErrorControl.Hooks.OnLLMFailure = func(context.Context, types.LLMFailureInfo) types.ErrorControlDecision {
		return types.ErrorControlDecision{Action: types.ErrorControlExtendIterations}
	}
	d = rt.DecideLLMFailure(context.Background(), types.LLMFailureInfo{Err: errors.New("x")}, logger.NoopLogger())
	require.Equal(t, types.ErrorControlAbort, d.Action)
}

func TestDecideLLMFailure_FallbackWithoutClientAborts(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		ErrorControl: &types.ErrorControlConfig{
			Hooks: types.AgentErrorHooks{
				OnLLMFailure: func(context.Context, types.LLMFailureInfo) types.ErrorControlDecision {
					return types.ErrorControlDecision{Action: types.ErrorControlFallbackModel}
				},
			},
		},
	})
	d := rt.DecideLLMFailure(context.Background(), types.LLMFailureInfo{Err: errors.New("x")}, logger.NoopLogger())
	require.Equal(t, types.ErrorControlAbort, d.Action)
}

func TestDecideMaxIterations_DefaultAndCap(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	d := rt.DecideMaxIterations(context.Background(), types.MaxIterationsInfo{MaxIterations: 3}, false, logger.NoopLogger())
	require.Equal(t, types.ErrorControlContinueWithFinalCall, d.Action)

	rt.AgentConfig.ErrorControl = &types.ErrorControlConfig{
		Hooks: types.AgentErrorHooks{
			OnMaxIterationsExceeded: func(context.Context, types.MaxIterationsInfo) types.ErrorControlDecision {
				return types.ErrorControlDecision{Action: types.ErrorControlExtendIterations, ExtraIterations: 100}
			},
		},
	}
	d = rt.DecideMaxIterations(context.Background(), types.MaxIterationsInfo{MaxIterations: 3}, false, logger.NoopLogger())
	require.Equal(t, types.ErrorControlExtendIterations, d.Action)
	require.Equal(t, 3, d.ExtraIterations)

	d = rt.DecideMaxIterations(context.Background(), types.MaxIterationsInfo{MaxIterations: 3}, true, logger.NoopLogger())
	require.Equal(t, types.ErrorControlContinueWithFinalCall, d.Action)
}

func TestClampAndEffectiveMaxIterations(t *testing.T) {
	require.Equal(t, 10, EffectiveMaxIterations(0))
	require.Equal(t, 4, EffectiveMaxIterations(4))
	require.Equal(t, 0, ClampExtraIterations(0, 5))
	require.Equal(t, 5, ClampExtraIterations(9, 5))
	require.Equal(t, 2, ClampExtraIterations(2, 5))
}
