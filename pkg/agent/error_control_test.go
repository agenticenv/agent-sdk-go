package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

type stubFallbackLLM struct{ model string }

func (s stubFallbackLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{}, nil
}
func (s stubFallbackLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, nil
}
func (s stubFallbackLLM) GetModel() string { return s.model }
func (s stubFallbackLLM) GetProvider() interfaces.LLMProvider {
	return interfaces.LLMProviderAnthropic
}
func (s stubFallbackLLM) IsStreamSupported() bool { return false }

func TestWithErrorControl_HooksStoredOnRuntimeConfig(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("err-hooks"),
		WithLLMClient(testLLM(t)),
		WithErrorControl(ErrorControlConfig{
			Hooks: AgentErrorHooks{
				OnLLMFailure: func(context.Context, LLMFailureInfo) ErrorControlDecision {
					return ErrorControlDecision{Action: ErrorControlFallbackModel}
				},
			},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := cfg.runtimeAgentConfig()
	if rt.ErrorControl == nil || rt.ErrorControl.Hooks.OnLLMFailure == nil {
		t.Fatal("expected OnLLMFailure on runtime ErrorControl")
	}
	if rt.ErrorControl.Hooks.OnMaxIterationsExceeded != nil {
		t.Fatal("expected nil OnMaxIterationsExceeded")
	}
}

func TestWithNamedLLMClients_StoredOnRuntimeConfig(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("named-llm"),
		WithLLMClient(testLLM(t)),
		WithNamedLLMClients(map[string]interfaces.LLMClient{
			"cheap": stubFallbackLLM{model: "claude-haiku"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := cfg.runtimeAgentConfig()
	got, ok := rt.NamedLLMClients["cheap"]
	if !ok || got.GetModel() != "claude-haiku" {
		t.Fatalf("NamedLLMClients = %+v", rt.NamedLLMClients)
	}
}

func TestWithNamedLLMClients_EmptyIgnored(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("named-empty"),
		WithLLMClient(testLLM(t)),
		WithNamedLLMClients(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.namedLLMClients != nil {
		t.Fatal("nil named clients should be ignored")
	}
}

func TestWithNamedLLMClients_RejectsEmptyName(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("named-blank"),
		WithLLMClient(testLLM(t)),
		WithNamedLLMClients(map[string]interfaces.LLMClient{"": stubFallbackLLM{model: "x"}}),
	})
	if err == nil || !strings.Contains(err.Error(), "name must not be empty") {
		t.Fatalf("got %v, want empty name error", err)
	}
}

func TestWithNamedLLMClients_RejectsNilClient(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("named-nil"),
		WithLLMClient(testLLM(t)),
		WithNamedLLMClients(map[string]interfaces.LLMClient{"cheap": nil}),
	})
	if err == nil || !strings.Contains(err.Error(), "cheap") {
		t.Fatalf("got %v, want nil client error", err)
	}
}

func TestWithErrorControl_FallbackNameMustExist(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("fb-missing"),
		WithLLMClient(testLLM(t)),
		WithErrorControl(ErrorControlConfig{FallbackLLMClient: "cheap"}),
	})
	if err == nil || !strings.Contains(err.Error(), "cheap") {
		t.Fatalf("got %v, want unknown Fallback error", err)
	}
}

func TestWithErrorControl_FallbackNameResolved(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("fb-ok"),
		WithLLMClient(testLLM(t)),
		WithNamedLLMClients(map[string]interfaces.LLMClient{
			"cheap": stubFallbackLLM{model: "claude-haiku"},
		}),
		WithErrorControl(ErrorControlConfig{FallbackLLMClient: "cheap"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := cfg.runtimeAgentConfig()
	if rt.ErrorControl == nil || rt.ErrorControl.FallbackLLMClient != "cheap" {
		t.Fatalf("FallbackLLMClient = %+v", rt.ErrorControl)
	}
	if rt.NamedLLMClients["cheap"].GetModel() != "claude-haiku" {
		t.Fatalf("named cheap = %+v", rt.NamedLLMClients)
	}
}

func TestWithErrorControl_EmptyFallbackAllowed(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("fb-empty"),
		WithLLMClient(testLLM(t)),
		WithErrorControl(ErrorControlConfig{
			Hooks: AgentErrorHooks{
				OnLLMFailure: func(context.Context, LLMFailureInfo) ErrorControlDecision {
					return ErrorControlDecision{Action: ErrorControlAbort}
				},
			},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.errorControl.FallbackLLMClient != "" {
		t.Fatal("empty FallbackLLMClient should be allowed")
	}
}

func TestWithErrorControl_CircuitBreakerStoredAndValidated(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("err-control"),
		WithLLMClient(testLLM(t)),
		WithErrorControl(ErrorControlConfig{
			CircuitBreaker: &CircuitBreakerConfig{
				MaxConsecutiveSameArgs: 3,
				PatternWindowSize:      4,
				ResetAfter:             2 * time.Minute,
			},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := cfg.runtimeAgentConfig()
	if rt.ErrorControl == nil || rt.ErrorControl.CircuitBreaker == nil {
		t.Fatal("expected circuit breaker on runtime config")
	}
	if rt.ErrorControl.CircuitBreaker.MaxConsecutiveSameArgs != 3 {
		t.Fatalf("MaxConsecutiveSameArgs = %d", rt.ErrorControl.CircuitBreaker.MaxConsecutiveSameArgs)
	}
}

func TestWithErrorControl_RejectsNegativeThresholds(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("err-control-bad"),
		WithLLMClient(testLLM(t)),
		WithErrorControl(ErrorControlConfig{
			CircuitBreaker: &CircuitBreakerConfig{MaxConsecutiveSameArgs: -1},
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "MaxConsecutiveSameArgs") {
		t.Fatalf("got %v, want MaxConsecutiveSameArgs error", err)
	}
}

func TestErrorControlFingerprint_EmptyWhenUnset(t *testing.T) {
	if got := errorControlFingerprint(nil); got != "" {
		t.Fatalf("nil config fingerprint = %q, want empty", got)
	}
	if got := errorControlFingerprint(&ErrorControlConfig{}); got != "" {
		t.Fatalf("empty config fingerprint = %q, want empty", got)
	}
}

func TestNamedLLMClientsFingerprint_EmptyWhenUnset(t *testing.T) {
	if got := namedLLMClientsFingerprint(nil); got != "" {
		t.Fatalf("nil named fingerprint = %q, want empty", got)
	}
}

func TestNamedLLMClientsFingerprint_NameOrderStable(t *testing.T) {
	a := namedLLMClientsFingerprint(map[string]interfaces.LLMClient{
		"cheap": stubFallbackLLM{model: "haiku"},
		"fast":  stubFallbackLLM{model: "sonnet"},
	})
	b := namedLLMClientsFingerprint(map[string]interfaces.LLMClient{
		"fast":  stubFallbackLLM{model: "sonnet"},
		"cheap": stubFallbackLLM{model: "haiku"},
	})
	if a == "" || a != b {
		t.Fatalf("named fingerprint should be order-stable: %q vs %q", a, b)
	}
}

func TestAgentConfigFingerprint_HooksChangesDigest(t *testing.T) {
	baseOpts := []Option{
		WithName("err-fp"),
		WithLLMClient(testLLM(t)),
	}
	cfgNone, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfgHooks, err := buildAgentConfig(append(baseOpts,
		WithErrorControl(ErrorControlConfig{
			Hooks: AgentErrorHooks{
				OnLLMFailure: func(context.Context, LLMFailureInfo) ErrorControlDecision {
					return ErrorControlDecision{Action: ErrorControlAbort}
				},
			},
		}),
	))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNone) == agentConfigFingerprint(cfgHooks) {
		t.Fatal("expected different fingerprints when hooks are set")
	}
}

func TestAgentConfigFingerprint_NamedLLMChangesDigest(t *testing.T) {
	baseOpts := []Option{
		WithName("named-fp"),
		WithLLMClient(testLLM(t)),
	}
	cfgNone, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfgNamed, err := buildAgentConfig(append(baseOpts, WithNamedLLMClients(map[string]interfaces.LLMClient{
		"cheap": stubFallbackLLM{model: "haiku"},
	})))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNone) == agentConfigFingerprint(cfgNamed) {
		t.Fatal("expected different fingerprints when named LLM clients are set")
	}
}

func TestAgentConfigFingerprint_FallbackNameChangesDigest(t *testing.T) {
	baseOpts := []Option{
		WithName("fb-fp"),
		WithLLMClient(testLLM(t)),
		WithNamedLLMClients(map[string]interfaces.LLMClient{
			"cheap": stubFallbackLLM{model: "haiku"},
			"fast":  stubFallbackLLM{model: "sonnet"},
		}),
	}
	cfgNone, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfgFB, err := buildAgentConfig(append(baseOpts, WithErrorControl(ErrorControlConfig{FallbackLLMClient: "cheap"})))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNone) == agentConfigFingerprint(cfgFB) {
		t.Fatal("expected different fingerprints when FallbackLLMClient is set")
	}
}

func TestAgentConfigFingerprint_CircuitBreakerChangesDigest(t *testing.T) {
	baseOpts := []Option{
		WithName("cb-fp"),
		WithLLMClient(testLLM(t)),
	}
	cfgNone, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfgCB, err := buildAgentConfig(append(baseOpts, WithErrorControl(ErrorControlConfig{
		CircuitBreaker: &CircuitBreakerConfig{MaxConsecutiveSameArgs: 3, PatternWindowSize: 4},
	})))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNone) == agentConfigFingerprint(cfgCB) {
		t.Fatal("expected different fingerprints when circuit breaker is set")
	}
}

func TestErrorControlFingerprint_HookBodiesNotHashed(t *testing.T) {
	h1 := &ErrorControlConfig{Hooks: AgentErrorHooks{OnLLMFailure: func(context.Context, LLMFailureInfo) ErrorControlDecision {
		return ErrorControlDecision{Action: ErrorControlAbort}
	}}}
	h2 := &ErrorControlConfig{Hooks: AgentErrorHooks{OnLLMFailure: func(context.Context, LLMFailureInfo) ErrorControlDecision {
		return ErrorControlDecision{Action: ErrorControlFallbackModel}
	}}}
	if errorControlFingerprint(h1) != errorControlFingerprint(h2) {
		t.Fatal("hook bodies must not change the fingerprint")
	}
}

func TestErrorControlFingerprint_BreakerThresholdsChangeDigest(t *testing.T) {
	a := errorControlFingerprint(&ErrorControlConfig{
		CircuitBreaker: &CircuitBreakerConfig{MaxConsecutiveSameArgs: 3, PatternWindowSize: 4},
	})
	b := errorControlFingerprint(&ErrorControlConfig{
		CircuitBreaker: &CircuitBreakerConfig{MaxConsecutiveSameArgs: 5, PatternWindowSize: 4},
	})
	c := errorControlFingerprint(&ErrorControlConfig{
		CircuitBreaker: &CircuitBreakerConfig{MaxConsecutiveSameArgs: 3, PatternWindowSize: 4, ResetAfter: time.Minute},
	})
	if a == "" || a == b {
		t.Fatalf("MaxConsecutiveSameArgs must change digest: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("ResetAfter must change digest")
	}
}

func TestErrorControlFingerprint_MaxIterationsSlotChangesDigest(t *testing.T) {
	none := errorControlFingerprint(&ErrorControlConfig{})
	maxIter := errorControlFingerprint(&ErrorControlConfig{
		Hooks: AgentErrorHooks{
			OnMaxIterationsExceeded: func(context.Context, MaxIterationsInfo) ErrorControlDecision {
				return ErrorControlDecision{Action: ErrorControlExtendIterations, ExtraIterations: 1}
			},
		},
	})
	if maxIter == "" || maxIter == none {
		t.Fatal("OnMaxIterationsExceeded slot must change digest")
	}
}
