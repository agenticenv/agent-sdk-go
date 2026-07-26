package agent

import (
	"strings"
	"testing"
)

func TestHasTemporalRuntime(t *testing.T) {
	var cfg agentConfig
	if cfg.hasTemporalRuntime() {
		t.Error("expected false without temporal backend")
	}
	cfg.temporalConfig = &TemporalConfig{TaskQueue: "q"}
	if !cfg.hasTemporalRuntime() {
		t.Error("expected true when TemporalConfig is set")
	}
}

func TestBuildAgentRuntime_NoTemporalBackend_BuildsLocalRuntime(t *testing.T) {
	// When no Temporal config is set, buildAgentRuntime falls back to LocalRuntime.
	cfg := &agentConfig{Name: "n", LLMClient: testLLM(t)}
	rt, err := cfg.buildAgentRuntime(false)
	if err != nil {
		t.Fatalf("expected local runtime to be built, got error: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil runtime")
	}
}

func TestBuildAgentRuntime_NoTemporalBackend_MissingLLMErrors(t *testing.T) {
	// Without an LLM client the local runtime builder must return an error.
	cfg := &agentConfig{Name: "n"}
	_, err := cfg.buildAgentRuntime(false)
	if err == nil || !strings.Contains(err.Error(), "llm client is required") {
		t.Fatalf("expected 'llm client is required', got %v", err)
	}
}
