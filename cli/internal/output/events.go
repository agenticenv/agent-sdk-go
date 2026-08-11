// Package output renders agent run events and usage summaries to the
// terminal. It is shared by the chat and run commands so both present
// tool calls, reasoning, and errors consistently.
package output

import (
	"encoding/json"
	"fmt"
	"strings"

	sdkagent "github.com/agenticenv/agent-sdk-go/pkg/agent"
)

// VerboseChatEvents reports whether logger.level enables chat UI detail
// (tool calls, args, results, thinking, steps). Errors and assistant text
// always print regardless of level.
func VerboseChatEvents(loggerLevel string) bool {
	return strings.EqualFold(strings.TrimSpace(loggerLevel), "debug")
}

// MarksStreamDelta reports whether ev carries streamed text/reasoning content,
// so callers can avoid double-printing the final result content.
func MarksStreamDelta(ev sdkagent.AgentEvent) bool {
	if ev == nil {
		return false
	}
	switch ev.Type() {
	case sdkagent.AgentEventTypeTextMessageContent, sdkagent.AgentEventTypeReasoningMessageContent:
		return true
	default:
		return false
	}
}

// RunResultFromFinishedEvent extracts the AgentRunResult from a run-finished
// event, or nil if ev is not a (recognizable) run-finished event.
func RunResultFromFinishedEvent(ev sdkagent.AgentEvent) *sdkagent.AgentRunResult {
	if ev == nil || ev.Type() != sdkagent.AgentEventTypeRunFinished {
		return nil
	}
	fin, ok := ev.(*sdkagent.AgentRunFinishedEvent)
	if !ok || fin == nil {
		return nil
	}
	return fin.Result
}

// PrintEvent writes a human-readable line for ev to stdout, skipping event
// types that are either purely internal or already reflected by streamed
// content (when streamedContent is true).
//
// When verbose is false (logger.level other than debug), tool/thinking/step
// lines are omitted so chat stays You:/assistant:-focused. Errors and
// non-streamed final content still print.
func PrintEvent(ev sdkagent.AgentEvent, streamedContent, verbose bool) {
	if ev == nil {
		return
	}
	switch ev.Type() {
	case sdkagent.AgentEventTypeCustom,
		sdkagent.AgentEventTypeTextMessageStart, sdkagent.AgentEventTypeTextMessageEnd,
		sdkagent.AgentEventTypeTextMessageContent,
		sdkagent.AgentEventTypeReasoningStart, sdkagent.AgentEventTypeReasoningEnd,
		sdkagent.AgentEventTypeReasoningMessageStart, sdkagent.AgentEventTypeReasoningMessageEnd,
		sdkagent.AgentEventTypeToolCallEnd, sdkagent.AgentEventTypeRunStarted:
		return
	case sdkagent.AgentEventTypeReasoningMessageContent:
		if !verbose {
			return
		}
		if r, ok := ev.(*sdkagent.AgentReasoningMessageContentEvent); ok && r.Delta != "" {
			fmt.Printf("[thinking] %s", r.Delta)
		}
	case sdkagent.AgentEventTypeToolCallStart:
		if !verbose {
			return
		}
		if t, ok := ev.(*sdkagent.AgentToolCallStartEvent); ok {
			fmt.Printf("\n[tool_call] %s (%s)\n", t.ToolCallName, t.ToolCallID)
		}
	case sdkagent.AgentEventTypeToolCallArgs:
		if !verbose {
			return
		}
		if t, ok := ev.(*sdkagent.AgentToolCallArgsEvent); ok && t.Delta != "" {
			fmt.Printf("[tool_args] %s %s\n", t.ToolCallID, t.Delta)
		}
	case sdkagent.AgentEventTypeToolCallResult:
		if !verbose {
			return
		}
		if t, ok := ev.(*sdkagent.AgentToolCallResultEvent); ok {
			fmt.Printf("[tool_result] %s: %s\n", t.ToolCallID, t.Content)
		}
	case sdkagent.AgentEventTypeRunError:
		if re, ok := ev.(*sdkagent.AgentRunErrorEvent); ok {
			fmt.Printf("[error] %s\n", re.Message)
		}
	case sdkagent.AgentEventTypeRunFinished:
		if res := RunResultFromFinishedEvent(ev); res != nil && res.Content != "" && !streamedContent {
			who := strings.TrimSpace(res.AgentName)
			if who == "" {
				who = "agent"
			}
			fmt.Printf("\n[%s complete] %s\n", who, res.Content)
		}
	case sdkagent.AgentEventTypeStepStarted:
		if !verbose {
			return
		}
		if t, ok := ev.(*sdkagent.AgentStepStartedEvent); ok && t.StepName != "" {
			fmt.Printf("\n[step] %s (sub-agent: %s)\n", ev.Type(), t.StepName)
		} else {
			fmt.Printf("\n[step] %s\n", ev.Type())
		}
	case sdkagent.AgentEventTypeStepFinished:
		if !verbose {
			return
		}
		if t, ok := ev.(*sdkagent.AgentStepFinishedEvent); ok && t.StepName != "" {
			fmt.Printf("[step] %s (sub-agent: %s)\n", ev.Type(), t.StepName)
		} else {
			fmt.Printf("[step] %s\n", ev.Type())
		}
	default:
		return
	}
}

// ToolArgsJSONIndented pretty-prints tool/delegation arguments for approval prompts.
func ToolArgsJSONIndented(args map[string]any) string {
	b, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// IsExitCommand reports whether s is a recognized chat-session exit keyword.
func IsExitCommand(s string) bool {
	switch strings.ToLower(s) {
	case "exit", "quit", "bye":
		return true
	}
	return false
}

// MergeLLMUsage accumulates add into acc, returning a new usage total.
func MergeLLMUsage(acc, add *sdkagent.LLMUsage) *sdkagent.LLMUsage {
	if add == nil {
		return acc
	}
	if acc == nil {
		cp := *add
		return &cp
	}
	return &sdkagent.LLMUsage{
		PromptTokens:       acc.PromptTokens + add.PromptTokens,
		CompletionTokens:   acc.CompletionTokens + add.CompletionTokens,
		TotalTokens:        acc.TotalTokens + add.TotalTokens,
		CachedPromptTokens: acc.CachedPromptTokens + add.CachedPromptTokens,
		ReasoningTokens:    acc.ReasoningTokens + add.ReasoningTokens,
	}
}

// PrintSessionUsageSummary writes a session-level token usage summary to stdout.
func PrintSessionUsageSummary(u *sdkagent.LLMUsage) {
	if u == nil {
		fmt.Println("\n[USAGE] no token usage reported this session")
		return
	}
	fmt.Println("\n[USAGE] session total")
	fmt.Printf("  prompt_tokens:     %d\n", u.PromptTokens)
	fmt.Printf("  completion_tokens: %d\n", u.CompletionTokens)
	fmt.Printf("  total_tokens:      %d\n", u.TotalTokens)
	if u.CachedPromptTokens > 0 {
		fmt.Printf("  cached_prompt:     %d\n", u.CachedPromptTokens)
	}
	if u.ReasoningTokens > 0 {
		fmt.Printf("  reasoning_tokens:  %d\n", u.ReasoningTokens)
	}
}
