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

func TestBuildAgentRuntime_NoTemporalBackend(t *testing.T) {
	cfg := &agentConfig{Name: "n", LLMClient: stubLLM{}}
	_, err := cfg.buildAgentRuntime(false)
	if err == nil || !strings.Contains(err.Error(), "no runtime configured") {
		t.Fatalf("got %v", err)
	}
}
