package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/guardrail"
)

func TestWithGuardrail_PreservesOrder(t *testing.T) {
	a := guardrail.New("a", allowGuardrail, guardrail.StageInput)
	b := guardrail.New("b", allowGuardrail, guardrail.StageOutput)
	cfg, err := buildAgentConfig([]Option{
		WithName("gr"),
		WithLLMClient(testLLM(t)),
		WithGuardrail(a),
		WithGuardrail(b),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.guardrails) != 2 || cfg.guardrails[0].Name() != "a" || cfg.guardrails[1].Name() != "b" {
		t.Fatalf("order = %v", namesOf(cfg.guardrails))
	}
	rt := cfg.runtimeAgentConfig()
	if len(rt.Guardrails) != 2 || rt.Guardrails[0].Name() != "a" {
		t.Fatalf("runtime order = %v", namesOf(rt.Guardrails))
	}
}

func TestWithGuardrail_Validation(t *testing.T) {
	cases := []struct {
		name string
		gs   []guardrail.Guardrail
		want string
	}{
		{"nil", []guardrail.Guardrail{nil}, "guardrail is nil"},
		{"empty name", []guardrail.Guardrail{guardrail.New("", allowGuardrail, guardrail.StageInput)}, "name is required"},
		{"duplicate", []guardrail.Guardrail{
			guardrail.New("a", allowGuardrail, guardrail.StageInput),
			guardrail.New("a", allowGuardrail, guardrail.StageOutput),
		}, `duplicate guardrail name "a"`},
		{"no stages", []guardrail.Guardrail{guardrail.New("a", allowGuardrail)}, "has no stages"},
		{"bad stage", []guardrail.Guardrail{guardrail.New("a", allowGuardrail, guardrail.Stage("tool"))}, "invalid stage"},
		{"duplicate stage", []guardrail.Guardrail{guardrail.New("a", allowGuardrail, guardrail.StageInput, guardrail.StageInput)}, "more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAgentConfig([]Option{
				WithName("gr"),
				WithLLMClient(testLLM(t)),
				WithGuardrail(tc.gs...),
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestWithGuardrail_AlongsideHooksNamedGuardrails(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("gr"),
		WithLLMClient(testLLM(t)),
		WithHooks("guardrails", AgentHooks{}),
		WithGuardrail(guardrail.New("pii", allowGuardrail, guardrail.StageInput)),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGuardrailsFingerprint_NameStageAndOrder(t *testing.T) {
	input := guardrail.New("pii", allowGuardrail, guardrail.StageInput)
	output := guardrail.New("pii", allowGuardrail, guardrail.StageOutput)
	other := guardrail.New("len", allowGuardrail, guardrail.StageInput)
	both := guardrail.New("pii", allowGuardrail, guardrail.StageOutput, guardrail.StageInput)
	base := guardrailsFingerprint([]guardrail.Guardrail{input})
	if base == "" {
		t.Fatal("expected digest")
	}
	if guardrailsFingerprint(nil) != "" {
		t.Fatal("empty list should hash to empty")
	}
	if guardrailsFingerprint([]guardrail.Guardrail{output}) == base {
		t.Fatal("stage should change the digest")
	}
	if guardrailsFingerprint([]guardrail.Guardrail{other}) == base {
		t.Fatal("name should change the digest")
	}
	if guardrailsFingerprint([]guardrail.Guardrail{both}) == base {
		t.Fatal("stage set should change the digest")
	}
	ab := guardrailsFingerprint([]guardrail.Guardrail{input, other})
	ba := guardrailsFingerprint([]guardrail.Guardrail{other, input})
	if ab == ba {
		t.Fatal("registration order should change the digest")
	}
	swapped := guardrail.New("pii", allowGuardrail, guardrail.StageInput, guardrail.StageOutput)
	if guardrailsFingerprint([]guardrail.Guardrail{both}) != guardrailsFingerprint([]guardrail.Guardrail{swapped}) {
		t.Fatal("stage order on one guardrail should not change the digest")
	}
}

func TestWithGuardrailExecutionConfig(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("gr"),
		WithLLMClient(testLLM(t)),
		WithGuardrailExecutionConfig(ExecutionConfig{Timeout: 2 * time.Minute, MaxAttempts: 1}),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := cfg.runtimeAgentConfig()
	if rt.ExecutionConfigs.Guardrail != (ExecutionConfig{Timeout: 2 * time.Minute, MaxAttempts: 1}) {
		t.Fatalf("override = %+v", rt.ExecutionConfigs.Guardrail)
	}
	resolved := runtime.ResolveExecutionPolicies(runtime.ExecutionConfigs{})
	if resolved.Guardrail.Timeout != 5*time.Minute || resolved.Guardrail.MaxAttempts != 3 {
		t.Fatalf("defaults = %+v", resolved.Guardrail)
	}
}

func allowGuardrail(context.Context, guardrail.Input) (guardrail.Decision, error) {
	return guardrail.Allow(), nil
}

func namesOf(gs []guardrail.Guardrail) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		if g != nil {
			out[i] = g.Name()
		}
	}
	return out
}
