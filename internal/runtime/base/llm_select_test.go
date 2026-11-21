package base

import (
	"context"
	"errors"
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/stretchr/testify/require"
)

func TestResolveLLM_NotConfiguredUsesPrimary(t *testing.T) {
	primary := stubLLMClient{resp: &interfaces.LLMResponse{Content: "primary"}}
	calls := 0
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: primary},
	})
	rt.AgentConfig.LLMSelector = &sdkruntime.LLMSelectorConfig{
		Select: func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
			calls++
			return "other", nil
		},
	}
	rt.AgentConfig.LLMSelector = nil

	result, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{
		Logger:    noopLog(),
		MessageID: "m",
		Messages:  []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	require.Equal(t, "primary", result.Content)
	require.Empty(t, result.LLMClientName)
	require.Equal(t, 0, calls)
}

func TestResolveLLM_PinnedPrimarySkipsSelector(t *testing.T) {
	primary := stubLLMClient{resp: &interfaces.LLMResponse{Content: "primary"}}
	calls := 0
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: primary},
		LLMSelector: &sdkruntime.LLMSelectorConfig{
			Select: func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
				calls++
				return "local", nil
			},
		},
	})

	result, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{
		Logger:             noopLog(),
		MessageID:          "m",
		LLMSelectionPinned: true,
		Messages:           []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	require.Equal(t, "primary", result.Content)
	require.Empty(t, result.LLMClientName)
	require.Equal(t, 0, calls)
}

func TestResolveLLM_NamedEmptyAndFallback(t *testing.T) {
	primary := &stubLLMClient{resp: &interfaces.LLMResponse{Content: "primary"}}
	local := &stubLLMClient{resp: &interfaces.LLMResponse{Content: "local"}}
	fallback := &stubLLMClient{resp: &interfaces.LLMResponse{Content: "fallback"}}
	var sawFinal bool
	calls := 0
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: primary},
		NamedLLMClients: map[string]interfaces.LLMClient{
			"local": local,
			"cheap": fallback,
		},
		ErrorControl: &types.ErrorControlConfig{FallbackLLMClient: "cheap"},
		LLMSelector: &sdkruntime.LLMSelectorConfig{
			Select: func(_ context.Context, info sdkruntime.LLMSelectInfo) (string, error) {
				calls++
				sawFinal = info.FinalCall
				if len(info.Request.Messages) > 0 && info.Request.Messages[0].Content == "secret" {
					return "local", nil
				}
				return "", nil
			},
		},
	})

	named, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{
		Logger:    noopLog(),
		MessageID: "m",
		Messages:  []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "secret"}},
	})
	require.NoError(t, err)
	require.Equal(t, "local", named.Content)
	require.Equal(t, "local", named.LLMClientName)

	primaryResult, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{
		Logger:    noopLog(),
		MessageID: "m2",
		FinalCall: true,
		Messages:  []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "plain"}},
	})
	require.NoError(t, err)
	require.Equal(t, "primary", primaryResult.Content)
	require.Empty(t, primaryResult.LLMClientName)
	require.True(t, sawFinal)

	fb, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{
		Logger:    noopLog(),
		MessageID: "m3",
		Client:    fallback,
		Messages:  []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "secret"}},
	})
	require.NoError(t, err)
	require.Equal(t, "fallback", fb.Content)
	require.Equal(t, "cheap", fb.LLMClientName)
	require.Equal(t, 2, calls)
}

func TestResolveLLM_UnknownSelectorErrorAndPanic(t *testing.T) {
	primary := stubLLMClient{resp: &interfaces.LLMResponse{Content: "primary"}}
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: primary},
		LLMSelector: &sdkruntime.LLMSelectorConfig{
			Select: func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
				return "missing", nil
			},
		},
	})
	_, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.ErrorContains(t, err, `llm select: client "missing" is not in WithNamedLLMClients`)
	require.True(t, IsLLMSelectAbort(err))

	rt.AgentConfig.LLMSelector.Select = func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
		return "", errors.New("boom")
	}
	_, err = rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.ErrorContains(t, err, "llm select: boom")
	require.True(t, IsLLMSelectAbort(err))

	rt.AgentConfig.LLMSelector.Select = func(context.Context, sdkruntime.LLMSelectInfo) (string, error) {
		panic("nope")
	}
	_, err = rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.ErrorContains(t, err, "llm select: selector panicked: nope")
	require.True(t, IsLLMSelectAbort(err))
}

func TestLLMCallSelection_PerRunPinsEmptyName(t *testing.T) {
	cfg := &sdkruntime.LLMSelectorConfig{Mode: sdkruntime.LLMSelectPerRun}
	sel := NewLLMCallSelection("").After(cfg, "", "")
	require.True(t, sel.Pinned)
	require.Empty(t, sel.Name)

	again := sel.After(cfg, "", "local")
	require.True(t, again.Pinned)
	require.Empty(t, again.Name)
	require.Equal(t, "local", again.Current)

	restored := RestoreLLMCallSelection("", true, "")
	require.True(t, restored.Pinned)
	require.Empty(t, restored.Name)
}
