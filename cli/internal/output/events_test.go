package output

import (
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	sdkagent "github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func TestMergeLLMUsage(t *testing.T) {
	if MergeLLMUsage(nil, nil) != nil {
		t.Fatal("expected nil")
	}
	a := &sdkagent.LLMUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, CachedPromptTokens: 4}
	got := MergeLLMUsage(nil, a)
	if got == nil || got.PromptTokens != 1 || got.CachedPromptTokens != 4 {
		t.Fatalf("nil+add = %#v", got)
	}
	b := &sdkagent.LLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30, ReasoningTokens: 5}
	got = MergeLLMUsage(a, b)
	if got.PromptTokens != 11 || got.CompletionTokens != 22 || got.TotalTokens != 33 || got.CachedPromptTokens != 4 || got.ReasoningTokens != 5 {
		t.Fatalf("merge = %#v", got)
	}
}

func TestIsExitCommand(t *testing.T) {
	// Callers trim input before calling IsExitCommand; the helper itself is case-insensitive, not TrimSpace.
	for _, s := range []string{"exit", "EXIT", "quit", "bye"} {
		if !IsExitCommand(s) {
			t.Errorf("IsExitCommand(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", " hello ", " quit ", "exiting", "n"} {
		if IsExitCommand(s) {
			t.Errorf("IsExitCommand(%q) = true, want false", s)
		}
	}
}

func TestToolArgsJSONIndented(t *testing.T) {
	out := ToolArgsJSONIndented(map[string]any{"a": 1, "b": "x"})
	if !strings.Contains(out, `"a"`) || !strings.Contains(out, `"b"`) {
		t.Fatalf("unexpected JSON: %s", out)
	}
	if out == "{}" {
		t.Fatal("expected non-empty indented JSON")
	}
}

func TestPrintEvent_smokeNoPanic(t *testing.T) {
	// Smoke: fmt to stdout; asserts wiring and nil-safety for branches used in the chat loop.
	PrintEvent(events.NewAgentTextMessageContentEvent("m1", "hi"), false)
	PrintEvent(events.NewAgentReasoningMessageContentEvent("m2", "d"), false)
	PrintEvent(events.NewAgentToolCallStartEvent("tid", "echo"), false)
	PrintEvent(events.NewAgentToolCallArgsEvent("tid", `{"q":"1"}`), false)
	PrintEvent(events.NewAgentToolCallResultEvent("m1", "tid", "ok"), false)
	PrintEvent(events.NewAgentRunErrorEvent("e"), false)
	PrintEvent(events.NewAgentRunFinishedEvent("", "", &types.AgentRunResult{Content: "done", AgentName: "A"}), false)
	PrintEvent(events.NewAgentRunFinishedEvent("", "", &types.AgentRunResult{Content: "done", AgentName: ""}), false)
	PrintEvent(events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), events.AgentCustomEventApprovalValue{
		ToolName: "echo", ApprovalToken: "tok",
	}), false)
	PrintEvent(events.NewAgentRunStartedEvent("t", "r"), false)
	PrintEvent(events.NewAgentTextMessageStartEvent("m", "assistant"), false)
	PrintEvent(events.NewAgentTextMessageEndEvent("m"), false)
	PrintEvent(events.NewBaseEvent(events.AgentEventType("UNKNOWN")), false)
}
