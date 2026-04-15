package main

import (
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func TestIsExitCommand(t *testing.T) {
	// main() trims input before calling isExitCommand; the helper itself is case-insensitive, not TrimSpace.
	for _, s := range []string{"exit", "EXIT", "quit", "bye"} {
		if !isExitCommand(s) {
			t.Errorf("isExitCommand(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", " hello ", " quit ", "exiting", "n"} {
		if isExitCommand(s) {
			t.Errorf("isExitCommand(%q) = true, want false", s)
		}
	}
}

func TestToolArgsJSONIndented(t *testing.T) {
	out := toolArgsJSONIndented(map[string]any{"a": 1, "b": "x"})
	if !strings.Contains(out, `"a"`) || !strings.Contains(out, `"b"`) {
		t.Fatalf("unexpected JSON: %s", out)
	}
	if out == "{}" {
		t.Fatal("expected non-empty indented JSON")
	}
}

func TestPrintEvent_smokeNoPanic(t *testing.T) {
	// Smoke: fmt to stdout; asserts wiring and nil-safety for branches used in the REPL loop.
	printEvent(&agent.AgentEvent{Type: agent.AgentEventContent, Content: "hi"}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventContentDelta, Content: "x"}, true)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventThinking, Content: "t"}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventThinkingDelta, Content: "d"}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventToolCall, ToolCall: &types.ToolCallEvent{ToolName: "echo", Args: map[string]any{"q": "1"}}}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventToolCall, ToolCall: &types.ToolCallEvent{ToolName: "echo"}}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventToolResult, ToolCall: &types.ToolCallEvent{ToolName: "echo", Result: "ok"}}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventError, Content: "e"}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventComplete, Content: "done", AgentName: "A"}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventComplete, Content: "done", AgentName: ""}, false)
	printEvent(&agent.AgentEvent{Type: agent.AgentEventApproval}, false)
	printEvent(&agent.AgentEvent{Type: types.AgentEventType("unknown")}, false)
}
