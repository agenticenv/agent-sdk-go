package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
)

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
	cfg := &agentConfig{Name: "n", LLMClient: testLLM(t)}
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
