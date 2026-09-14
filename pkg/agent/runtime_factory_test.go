package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/local"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
)

// noDurability disables durable-go for tests that only care about runtime *selection*
// (Temporal vs local), not durability — keeps them from creating a real durable-go
// engine (and an "agent_data/<name>" journal directory) as a side effect.
func noDurability() *local.LocalConfig {
	off := false
	return &local.LocalConfig{Durability: &off}
}

func TestHasTemporalRuntime(t *testing.T) {
	var cfg agentConfig
	if cfg.hasTemporalRuntime() {
		t.Error("expected false without temporal runtime")
	}
	cfg.runtimeFactory = &stubTemporalFactory{queue: "q"}
	if !cfg.hasTemporalRuntime() {
		t.Error("expected true when Temporal factory is set")
	}
}

func TestHasRestateRuntime(t *testing.T) {
	var cfg agentConfig
	if cfg.hasRestateRuntime() {
		t.Error("expected false without restate runtime")
	}
	cfg.runtimeFactory = &stubRestateFactory{}
	if !cfg.hasRestateRuntime() {
		t.Error("expected true when Restate factory is set")
	}
}

func TestBuildAgentRuntime_NoTemporalFactory_BuildsLocalRuntime(t *testing.T) {
	cfg := &agentConfig{Name: "n", LLMClient: testLLM(t), localConfig: noDurability()}
	rt, err := cfg.buildAgentRuntime(false)
	if err != nil {
		t.Fatalf("expected local runtime to be built, got error: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil runtime")
	}
}

func TestBuildAgentRuntime_NoTemporalFactory_MissingLLMErrors(t *testing.T) {
	cfg := &agentConfig{Name: "n"}
	_, err := cfg.buildAgentRuntime(false)
	if err == nil || !strings.Contains(err.Error(), "llm client is required") {
		t.Fatalf("expected 'llm client is required', got %v", err)
	}
}

func TestWithLocalConfig_ConflictsWithRuntimeFactory(t *testing.T) {
	cfg := &agentConfig{}
	withTestTemporal("q")(cfg)
	withLocalConfig(noDurability())(cfg)
	if cfg.factoryConflict == nil || !strings.Contains(cfg.factoryConflict.Error(), "incompatible") {
		t.Fatalf("expected incompatible conflict error, got %v", cfg.factoryConflict)
	}
}

func TestWithLocalConfig_ConflictsWithRuntimeFactory_ReverseOrder(t *testing.T) {
	cfg := &agentConfig{}
	withLocalConfig(noDurability())(cfg)
	withTestTemporal("q")(cfg)
	if cfg.factoryConflict == nil || !strings.Contains(cfg.factoryConflict.Error(), "incompatible") {
		t.Fatalf("expected incompatible conflict error, got %v", cfg.factoryConflict)
	}
}

func TestWithLocalConfig_NilIsNoop(t *testing.T) {
	cfg := &agentConfig{}
	withLocalConfig(nil)(cfg)
	if cfg.localConfig != nil || cfg.factoryConflict != nil {
		t.Fatalf("expected nil cfg to be a no-op, got localConfig=%v factoryConflict=%v", cfg.localConfig, cfg.factoryConflict)
	}
}

// withTestTemporal is a test-only Option that selects a Temporal-named factory without
// importing pkg/agent/runtime/temporal (avoids an import cycle with agent tests).
func withTestTemporal(queue string) Option {
	return withRuntimeFactory(&stubTemporalFactory{queue: queue})
}

type stubTemporalFactory struct {
	queue string
	rt    runtime.Runtime
}

func (s *stubTemporalFactory) Name() string { return "temporal" }
func (s *stubTemporalFactory) Validate() error {
	if s.queue == "" {
		return errors.New("TaskQueue is required in TemporalConfig: provide a unique name per agent")
	}
	return nil
}
func (s *stubTemporalFactory) Build(*agentruntime.RuntimeParams, bool) (runtime.Runtime, error) {
	return s.rt, nil
}

type stubRestateFactory struct{}

func (s *stubRestateFactory) Name() string    { return "restate" }
func (s *stubRestateFactory) Validate() error { return nil }
func (s *stubRestateFactory) Build(*agentruntime.RuntimeParams, bool) (runtime.Runtime, error) {
	return nil, nil
}
