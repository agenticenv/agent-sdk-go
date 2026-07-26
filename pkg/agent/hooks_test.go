package agent

import (
	"context"
	"strings"
	"testing"
)

func TestWithHooks_MergesInDeclarationOrder(t *testing.T) {
	h1 := func(context.Context, BeforeLLMHookInput) (BeforeLLMHookOutput, error) {
		return BeforeLLMHookOutput{}, nil
	}
	h2 := func(context.Context, BeforeLLMHookInput) (BeforeLLMHookOutput, error) {
		return BeforeLLMHookOutput{}, nil
	}
	h3 := func(context.Context, AfterToolHookInput) (AfterToolHookOutput, error) {
		return AfterToolHookOutput{}, nil
	}

	cfg, err := buildAgentConfig([]Option{
		WithName("hooks"),
		WithLLMClient(testLLM(t)),
		WithHooks("guardrails", AgentHooks{BeforeLLM: []BeforeLLMHook{h1}}),
		WithHooks("audit", AgentHooks{
			BeforeLLM: []BeforeLLMHook{h2},
			AfterTool: []AfterToolHook{h3},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	merged := cfg.mergedHooks()
	if len(merged.BeforeLLM) != 2 {
		t.Fatalf("BeforeLLM len = %d, want 2", len(merged.BeforeLLM))
	}
	if merged.BeforeLLM[0] == nil || merged.BeforeLLM[1] == nil {
		t.Fatal("expected non-nil BeforeLLM hooks")
	}
	if len(merged.AfterTool) != 1 {
		t.Fatalf("AfterTool len = %d, want 1", len(merged.AfterTool))
	}
	if len(cfg.hooks) != 2 || cfg.hooks[0].Name != "guardrails" || cfg.hooks[1].Name != "audit" {
		t.Fatalf("hooks = %#v", cfg.hooks)
	}
}

func TestWithHooks_RequiresName(t *testing.T) {
	h := func(context.Context, BeforeRetrieveHookInput) (BeforeRetrieveHookOutput, error) {
		return BeforeRetrieveHookOutput{}, nil
	}
	_, err := buildAgentConfig([]Option{
		WithName("hooks-empty"),
		WithLLMClient(testLLM(t)),
		WithHooks("", AgentHooks{BeforeRetrieve: []BeforeRetrieveHook{h}}),
	})
	if err == nil || !strings.Contains(err.Error(), "hook group name is required") {
		t.Fatalf("expected name required error, got %v", err)
	}
}

func TestWithHooks_RejectsDuplicateName(t *testing.T) {
	h := func(context.Context, BeforeRetrieveHookInput) (BeforeRetrieveHookOutput, error) {
		return BeforeRetrieveHookOutput{}, nil
	}
	_, err := buildAgentConfig([]Option{
		WithName("hooks-dup"),
		WithLLMClient(testLLM(t)),
		WithHooks("audit", AgentHooks{BeforeRetrieve: []BeforeRetrieveHook{h}}),
		WithHooks("audit", AgentHooks{}),
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate hook group name "audit"`) {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestRuntimeAgentConfig_PassesHookGroups(t *testing.T) {
	h := func(context.Context, BeforeLLMHookInput) (BeforeLLMHookOutput, error) {
		return BeforeLLMHookOutput{}, nil
	}
	cfg, err := buildAgentConfig([]Option{
		WithName("hooks-runtime"),
		WithLLMClient(testLLM(t)),
		WithHooks("guardrails", AgentHooks{BeforeLLM: []BeforeLLMHook{h}}),
		WithHooks("audit", AgentHooks{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := cfg.runtimeAgentConfig()
	if len(rt.Hooks) != 2 {
		t.Fatalf("Hooks len = %d, want 2", len(rt.Hooks))
	}
	if rt.Hooks[0].Name != "guardrails" || len(rt.Hooks[0].Hooks.BeforeLLM) != 1 {
		t.Fatalf("Hooks[0] = %#v", rt.Hooks[0])
	}
	if rt.Hooks[1].Name != "audit" {
		t.Fatalf("Hooks[1].Name = %q, want audit", rt.Hooks[1].Name)
	}
}

func TestHookGroupsFingerprint_emptyWhenNoGroups(t *testing.T) {
	if got := hookGroupsFingerprint(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestHookGroupsFingerprint_sortedNamesStable(t *testing.T) {
	fpAB := hookGroupsFingerprint([]HookGroup{{Name: "audit"}, {Name: "guardrails"}})
	fpBA := hookGroupsFingerprint([]HookGroup{{Name: "guardrails"}, {Name: "audit"}})
	if fpAB == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if fpAB != fpBA {
		t.Fatalf("registration order should not matter: %q vs %q", fpAB, fpBA)
	}
}

func TestHookGroupsFingerprint_differentNamesDifferentDigest(t *testing.T) {
	fpOne := hookGroupsFingerprint([]HookGroup{{Name: "guardrails"}})
	fpTwo := hookGroupsFingerprint([]HookGroup{{Name: "guardrails"}, {Name: "audit"}})
	if fpOne == fpTwo {
		t.Fatal("expected different fingerprints for different hook group sets")
	}
}

func TestAgentConfigFingerprint_HookGroupsChangesDigest(t *testing.T) {
	h := func(context.Context, BeforeLLMHookInput) (BeforeLLMHookOutput, error) {
		return BeforeLLMHookOutput{}, nil
	}
	baseOpts := []Option{
		WithName("hooks-fp"),
		WithLLMClient(testLLM(t)),
	}
	cfgNoHooks, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfgWithHooks, err := buildAgentConfig(append(baseOpts,
		WithHooks("guardrails", AgentHooks{BeforeLLM: []BeforeLLMHook{h}}),
	))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNoHooks) == agentConfigFingerprint(cfgWithHooks) {
		t.Fatal("expected different fingerprints when hook groups are configured")
	}
}
