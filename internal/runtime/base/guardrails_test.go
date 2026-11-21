package base

import (
	"context"
	"errors"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/hooks"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/guardrail"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/stretchr/testify/require"
)

func TestEvaluateGuardrails_ModifyFeedsNextAndDenyStops(t *testing.T) {
	var second int
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Guardrails: []guardrail.Guardrail{
			guardrail.New("one", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
				return guardrail.Modify("next", "changed"), nil
			}, guardrail.StageInput),
			guardrail.New("two", func(_ context.Context, in guardrail.Input) (guardrail.Decision, error) {
				second++
				if in.Text != "next" {
					t.Fatalf("second saw %q", in.Text)
				}
				return guardrail.Deny("stop"), nil
			}, guardrail.StageInput),
			guardrail.New("three", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
				t.Fatal("third should not run")
				return guardrail.Allow(), nil
			}, guardrail.StageInput),
		},
	})
	_, err := rt.ApplyInputGuardrails(context.Background(), ApplyInputGuardrailsInput{Text: "raw"})
	require.ErrorIs(t, err, types.ErrGuardrailBlocked)
	require.Equal(t, 1, second)
	var ge *guardrail.Error
	require.ErrorAs(t, err, &ge)
	require.Equal(t, "two", ge.Guardrail)
}

func TestEvaluateGuardrails_FailClosedIsNotBlocked(t *testing.T) {
	boom := errors.New("upstream")
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Guardrails: []guardrail.Guardrail{
			guardrail.New("bad", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
				return guardrail.Deny("nope"), boom
			}, guardrail.StageInput),
		},
	})
	_, err := rt.ApplyInputGuardrails(context.Background(), ApplyInputGuardrailsInput{Text: "x"})
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, types.ErrGuardrailBlocked)
	require.Contains(t, err.Error(), `agent: guardrail "bad" (input): evaluate:`)

	rt.AgentConfig.Guardrails = []guardrail.Guardrail{
		guardrail.New("empty", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
			return guardrail.Decision{}, nil
		}, guardrail.StageInput),
	}
	_, err = rt.ApplyInputGuardrails(context.Background(), ApplyInputGuardrailsInput{Text: "x"})
	require.NotErrorIs(t, err, types.ErrGuardrailBlocked)
	require.Contains(t, err.Error(), "empty action")

	rt.AgentConfig.Guardrails = []guardrail.Guardrail{
		guardrail.New("odd", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
			return guardrail.Decision{Action: "drop"}, nil
		}, guardrail.StageInput),
	}
	_, err = rt.ApplyInputGuardrails(context.Background(), ApplyInputGuardrailsInput{Text: "x"})
	require.NotErrorIs(t, err, types.ErrGuardrailBlocked)
	require.Contains(t, err.Error(), `unknown action "drop"`)
}

func TestRunAfterLLM_SeesHookTextAndRunsWithoutHooks(t *testing.T) {
	var seen string
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{resp: &interfaces.LLMResponse{Content: "raw"}}},
		Hooks: []hooks.HookGroup{{
			Name: "rewrite",
			Hooks: hooks.AgentHooks{AfterLLM: []hooks.AfterLLMHook{
				func(_ context.Context, in hooks.AfterLLMHookInput) (hooks.AfterLLMHookOutput, error) {
					in.Response.Content = "hooked"
					return hooks.AfterLLMHookOutput{Response: in.Response}, nil
				},
			}},
		}},
		Guardrails: []guardrail.Guardrail{
			guardrail.New("see", func(_ context.Context, in guardrail.Input) (guardrail.Decision, error) {
				seen = in.Text
				return guardrail.Modify(in.Text+"!", "marked"), nil
			}, guardrail.StageOutput),
		},
	})
	result, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.NoError(t, err)
	require.Equal(t, "hooked", seen)
	require.Equal(t, "hooked!", result.Content)

	rt.AgentConfig.Hooks = nil
	rt.AgentConfig.LLM.Client = stubLLMClient{resp: &interfaces.LLMResponse{Content: "raw"}}
	result, err = rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.NoError(t, err)
	require.Equal(t, "raw!", result.Content)
}

func TestRunAfterLLM_HookDoesNotBypassDenyOrModify(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{resp: &interfaces.LLMResponse{Content: "raw"}}},
		Hooks: []hooks.HookGroup{{
			Name: "rewrite",
			Hooks: hooks.AgentHooks{AfterLLM: []hooks.AfterLLMHook{
				func(_ context.Context, in hooks.AfterLLMHookInput) (hooks.AfterLLMHookOutput, error) {
					in.Response.Content = "safe"
					return hooks.AfterLLMHookOutput{Response: in.Response}, nil
				},
			}},
		}},
		Guardrails: []guardrail.Guardrail{
			guardrail.New("deny-safe", func(_ context.Context, in guardrail.Input) (guardrail.Decision, error) {
				if in.Text == "safe" {
					return guardrail.Deny("blocked"), nil
				}
				return guardrail.Allow(), nil
			}, guardrail.StageOutput),
		},
	})
	_, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.ErrorIs(t, err, types.ErrGuardrailBlocked)
}

func TestRunAfterLLM_ToolCallsChangeFinal(t *testing.T) {
	var finals []bool
	jsonG := guardrail.JSON("json")
	record := guardrail.New("final", func(_ context.Context, in guardrail.Input) (guardrail.Decision, error) {
		finals = append(finals, in.Final)
		return guardrail.Allow(), nil
	}, guardrail.StageOutput)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{resp: &interfaces.LLMResponse{
			Content:   "not-json",
			ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c", ToolName: "t"}},
		}}},
		Hooks: []hooks.HookGroup{{
			Name: "drop-tools",
			Hooks: hooks.AgentHooks{AfterLLM: []hooks.AfterLLMHook{
				func(_ context.Context, in hooks.AfterLLMHookInput) (hooks.AfterLLMHookOutput, error) {
					in.Response.ToolCalls = nil
					return hooks.AfterLLMHookOutput{Response: in.Response}, nil
				},
			}},
		}},
		Guardrails: []guardrail.Guardrail{record, jsonG},
	})
	_, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.ErrorIs(t, err, types.ErrGuardrailBlocked)
	require.Equal(t, []bool{true}, finals)

	finals = nil
	rt.AgentConfig.LLM.Client = stubLLMClient{resp: &interfaces.LLMResponse{Content: "not-json"}}
	rt.AgentConfig.Hooks = []hooks.HookGroup{{
		Name: "add-tools",
		Hooks: hooks.AgentHooks{AfterLLM: []hooks.AfterLLMHook{
			func(_ context.Context, in hooks.AfterLLMHookInput) (hooks.AfterLLMHookOutput, error) {
				in.Response.ToolCalls = []*interfaces.ToolCall{{ToolCallID: "c", ToolName: "t"}}
				return hooks.AfterLLMHookOutput{Response: in.Response}, nil
			},
		}},
	}}
	result, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.NoError(t, err)
	require.Equal(t, []bool{false}, finals)
	require.Equal(t, "not-json", result.Content)
}

func TestRunAfterLLM_SkipsEmptyToolCallTurn(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{resp: &interfaces.LLMResponse{
			ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c", ToolName: "t"}},
		}}},
		Guardrails: []guardrail.Guardrail{
			guardrail.New("deny-all", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
				return guardrail.Deny("no"), nil
			}, guardrail.StageOutput),
		},
	})
	result, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
}

func TestExecuteLLMStream_HoldsTextWhenOutputGuardrailRegistered(t *testing.T) {
	s := newFixedStream([]*interfaces.LLMStreamChunk{
		{ThinkingDelta: "think"},
		{ContentDelta: "hello"},
		{ContentDelta: " world"},
	}, &interfaces.LLMResponse{Content: "hello world"})
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
		Guardrails: []guardrail.Guardrail{
			guardrail.New("mark", func(_ context.Context, in guardrail.Input) (guardrail.Decision, error) {
				return guardrail.Modify("checked:"+in.Text, "redacted"), nil
			}, guardrail.StageOutput),
		},
	})
	var got []events.AgentEvent
	_, err := rt.ExecuteLLMStream(context.Background(), ExecuteLLMInput{
		Logger: noopLog(), MessageID: "m",
		Emit: func(ev events.AgentEvent) { got = append(got, ev) },
	})
	require.NoError(t, err)
	var texts []string
	for _, ev := range got {
		if c, ok := ev.(*events.AgentTextMessageContentEvent); ok {
			texts = append(texts, c.Delta)
		}
	}
	require.Equal(t, []string{"checked:hello world"}, texts)
	var sawReasoning bool
	for _, ev := range got {
		if ev.Type() == events.AgentEventTypeReasoningMessageContent {
			sawReasoning = true
		}
	}
	require.True(t, sawReasoning)
}

func TestExecuteLLMStream_InputGuardrailDoesNotHoldDeltas(t *testing.T) {
	s := newFixedStream([]*interfaces.LLMStreamChunk{
		{ContentDelta: "hello"},
		{ContentDelta: " world"},
	}, &interfaces.LLMResponse{Content: "hello world"})
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
		Guardrails: []guardrail.Guardrail{
			guardrail.New("in", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
				return guardrail.Allow(), nil
			}, guardrail.StageInput),
		},
	})
	var texts []string
	_, err := rt.ExecuteLLMStream(context.Background(), ExecuteLLMInput{
		Logger: noopLog(), MessageID: "m",
		Emit: func(ev events.AgentEvent) {
			if c, ok := ev.(*events.AgentTextMessageContentEvent); ok {
				texts = append(texts, c.Delta)
			}
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"hello", " world"}, texts)
}

func TestExecuteLLM_DenyCountsAsCompletedCall(t *testing.T) {
	metrics := &countMetrics{}
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{resp: &interfaces.LLMResponse{
			Content: "nope",
			Usage:   &interfaces.LLMUsage{PromptTokens: 3, CompletionTokens: 4},
		}}},
		Guardrails: []guardrail.Guardrail{
			guardrail.New("deny", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
				return guardrail.Deny("no"), nil
			}, guardrail.StageOutput),
		},
	})
	rt.Metrics = metrics
	_, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.ErrorIs(t, err, types.ErrGuardrailBlocked)
	require.Equal(t, 1, metrics.counters[types.MetricLLMCallCompleted])
	require.Equal(t, 0, metrics.counters[types.MetricLLMCallFailed])
	require.Equal(t, 1, metrics.histograms[types.MetricLLMTokensOutput])
	require.Equal(t, 1, metrics.counters[types.MetricGuardrailEvaluated])

	metrics = &countMetrics{}
	rt.Metrics = metrics
	rt.AgentConfig.Guardrails = []guardrail.Guardrail{
		guardrail.New("boom", func(context.Context, guardrail.Input) (guardrail.Decision, error) {
			return guardrail.Decision{}, errors.New("down")
		}, guardrail.StageOutput),
	}
	_, err = rt.ExecuteLLM(context.Background(), ExecuteLLMInput{Logger: noopLog(), MessageID: "m"})
	require.Error(t, err)
	require.NotErrorIs(t, err, types.ErrGuardrailBlocked)
	require.Equal(t, 1, metrics.counters[types.MetricLLMCallFailed])
	require.Equal(t, 0, metrics.counters[types.MetricLLMCallCompleted])
	require.Equal(t, 0, metrics.histograms[types.MetricLLMTokensOutput])
}

type countMetrics struct {
	counters   map[string]int
	histograms map[string]int
}

func (m *countMetrics) IncrementCounter(_ context.Context, name string, _ ...interfaces.Attribute) {
	if m.counters == nil {
		m.counters = map[string]int{}
	}
	m.counters[name]++
}

func (m *countMetrics) RecordHistogram(_ context.Context, name string, _ float64, _ ...interfaces.Attribute) {
	if m.histograms == nil {
		m.histograms = map[string]int{}
	}
	m.histograms[name]++
}

func (m *countMetrics) Shutdown(context.Context) error { return nil }
