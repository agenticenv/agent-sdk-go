package main

import (
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func TestMergeLLMUsage(t *testing.T) {
	if mergeLLMUsage(nil, nil) != nil {
		t.Fatal("expected nil")
	}
	a := &agent.LLMUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, CachedPromptTokens: 4}
	got := mergeLLMUsage(nil, a)
	if got == nil || got.PromptTokens != 1 || got.CachedPromptTokens != 4 {
		t.Fatalf("nil+add = %#v", got)
	}
	b := &agent.LLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30, ReasoningTokens: 5}
	got = mergeLLMUsage(a, b)
	if got.PromptTokens != 11 || got.CompletionTokens != 22 || got.TotalTokens != 33 || got.CachedPromptTokens != 4 || got.ReasoningTokens != 5 {
		t.Fatalf("merge = %#v", got)
	}
}

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
	printEvent(events.NewAgentTextMessageContentEvent("m1", "hi"), false)
	printEvent(events.NewAgentReasoningMessageContentEvent("m2", "d"), false)
	printEvent(events.NewAgentToolCallStartEvent("tid", "echo"), false)
	printEvent(events.NewAgentToolCallArgsEvent("tid", `{"q":"1"}`), false)
	printEvent(events.NewAgentToolCallResultEvent("m1", "tid", "ok"), false)
	printEvent(events.NewAgentRunErrorEvent("e"), false)
	printEvent(events.NewAgentRunFinishedEvent("", "", &types.AgentRunResult{Content: "done", AgentName: "A"}), false)
	printEvent(events.NewAgentRunFinishedEvent("", "", &types.AgentRunResult{Content: "done", AgentName: ""}), false)
	printEvent(events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), events.AgentCustomEventApprovalValue{
		ToolName: "echo", ApprovalToken: "tok",
	}), false)
	printEvent(events.NewAgentRunStartedEvent("t", "r"), false)
	printEvent(events.NewAgentTextMessageStartEvent("m", "assistant"), false)
	printEvent(events.NewAgentTextMessageEndEvent("m"), false)
	printEvent(events.NewBaseEvent(events.AgentEventType("UNKNOWN")), false)
}
