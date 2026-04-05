package temporal

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func TestGetEventTaskQueue(t *testing.T) {
	if got := getEventTaskQueue("my-queue"); got != "my-queue-events" {
		t.Errorf("getEventTaskQueue(%q) = %q, want my-queue-events", "my-queue", got)
	}
}

func TestStreamCompleteEndsRun(t *testing.T) {
	root := "Main agent"
	if streamCompleteEndsRun(nil, root) {
		t.Error("nil event should not end run")
	}
	if streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventContent}, root) {
		t.Error("non-complete should not end run")
	}
	if !streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventComplete, AgentName: ""}, root) {
		t.Error("complete with empty agent should end run (legacy)")
	}
	if !streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventComplete, AgentName: root}, root) {
		t.Error("complete from root should end run")
	}
	if streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventComplete, AgentName: "MathSpecialist"}, root) {
		t.Error("complete from sub-agent should not end root run")
	}
}

func TestSubAgentQueryFromArgs(t *testing.T) {
	if subAgentQueryFromArgs(nil) != "" {
		t.Error("nil args")
	}
	if subAgentQueryFromArgs(map[string]any{}) != "" {
		t.Error("empty map")
	}
	if got := subAgentQueryFromArgs(map[string]any{"query": "hello"}); got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestAgent_BeginRunEndRun(t *testing.T) {
	l := logger.DefaultLogger("error")
	a := &TemporalRuntime{TemporalRuntimeConfig: TemporalRuntimeConfig{logger: l}}

	cleanup, err := a.beginRun("wf1")
	if err != nil {
		t.Fatalf("beginRun: %v", err)
	}
	cleanup()

	_, err = a.beginRun("wf1")
	if err != nil {
		t.Fatalf("beginRun after cleanup: %v", err)
	}
	_, err = a.beginRun("wf2")
	if err != ErrAgentAlreadyRunning {
		t.Errorf("beginRun concurrent: got %v, want ErrAgentAlreadyRunning", err)
	}
}
