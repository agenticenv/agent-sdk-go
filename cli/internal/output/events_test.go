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

func TestVerboseChatEvents(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  bool
	}{
		{"debug", true},
		{"DEBUG", true},
		{" debug ", true},
		{"info", false},
		{"error", false},
		{"", false},
	} {
		if got := VerboseChatEvents(tc.level); got != tc.want {
			t.Errorf("VerboseChatEvents(%q) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

func TestPrintEvent_smokeNoPanic(t *testing.T) {
	// Smoke: fmt to stdout; asserts wiring and nil-safety for branches used in the chat loop.
	for _, verbose := range []bool{false, true} {
		PrintEvent(events.NewAgentTextMessageContentEvent("m1", "hi"), false, verbose)
		PrintEvent(events.NewAgentReasoningMessageContentEvent("m2", "d"), false, verbose)
		PrintEvent(events.NewAgentToolCallStartEvent("tid", "echo"), false, verbose)
		PrintEvent(events.NewAgentToolCallArgsEvent("tid", `{"q":"1"}`), false, verbose)
		PrintEvent(events.NewAgentToolCallResultEvent("m1", "tid", "ok"), false, verbose)
		PrintEvent(events.NewAgentRunErrorEvent("e"), false, verbose)
		PrintEvent(events.NewAgentRunFinishedEvent("", "", &types.AgentRunResult{Content: "done", AgentName: "A"}), false, verbose)
		PrintEvent(events.NewAgentRunFinishedEvent("", "", &types.AgentRunResult{Content: "done", AgentName: ""}), false, verbose)
		PrintEvent(events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), events.AgentCustomEventApprovalValue{
			ToolName: "echo", ApprovalToken: "tok",
		}), false, verbose)
		PrintEvent(events.NewAgentRunStartedEvent("t", "r"), false, verbose)
		PrintEvent(events.NewAgentTextMessageStartEvent("m", "assistant"), false, verbose)
		PrintEvent(events.NewAgentTextMessageEndEvent("m"), false, verbose)
		PrintEvent(events.NewBaseEvent(events.AgentEventType("UNKNOWN")), false, verbose)
	}
}
