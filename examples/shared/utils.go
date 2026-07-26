package shared

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

// RunResultFromFinishedEvent returns the typed result from a RUN_FINISHED event, or nil.
func RunResultFromFinishedEvent(ev agent.AgentEvent) *agent.AgentRunResult {
	if ev == nil || ev.Type() != agent.AgentEventTypeRunFinished {
		return nil
	}
	fin, ok := ev.(*agent.AgentRunFinishedEvent)
	if !ok || fin == nil {
		return nil
	}
	return fin.Result
}

// ToolApprovalValueFromEvent returns the CUSTOM tool-approval payload when ev is that stream event.
func ToolApprovalValueFromEvent(ev agent.AgentEvent) (agent.AgentCustomEventApprovalValue, bool) {
	ce, ok := ev.(*agent.AgentCustomEvent)
	if !ok || ce == nil || ce.Name != string(agent.AgentCustomEventNameToolApproval) {
		return agent.AgentCustomEventApprovalValue{}, false
	}
	v, err := agent.ParseCustomEventApproval(ce)
	if err != nil || v.ApprovalToken == "" {
		return agent.AgentCustomEventApprovalValue{}, false
	}
	return v, true
}

// DelegationApprovalValueFromEvent returns the CUSTOM sub-agent delegation payload when ev is that stream event.
func DelegationApprovalValueFromEvent(ev agent.AgentEvent) (agent.AgentCustomEventDelegationValue, bool) {
	ce, ok := ev.(*agent.AgentCustomEvent)
	if !ok || ce == nil || ce.Name != string(agent.AgentCustomEventNameSubAgentDelegation) {
		return agent.AgentCustomEventDelegationValue{}, false
	}
	v, err := agent.ParseCustomEventDelegation(ce)
	if err != nil || v.ApprovalToken == "" {
		return agent.AgentCustomEventDelegationValue{}, false
	}
	return v, true
}

// MarksStreamDelta returns true when the event carries assistant or reasoning text deltas.
func MarksStreamDelta(ev agent.AgentEvent) bool {
	if ev == nil {
		return false
	}
	switch ev.Type() {
	case agent.AgentEventTypeTextMessageContent, agent.AgentEventTypeReasoningMessageContent:
		return true
	default:
		return false
	}
}

// ShowLLMUsage reports whether examples should print token usage (SHOW_LLM_USAGE; default false).
func ShowLLMUsage() bool {
	return envBool("SHOW_LLM_USAGE")
}

// ShowTelemetry reports whether examples should print run telemetry (SHOW_TELEMETRY; default false).
func ShowTelemetry() bool {
	return envBool("SHOW_TELEMETRY")
}

// RunIDLine returns a formatted line for printing the run ID at stream or async-run start.
// Callers should print this immediately after receiving the runID from Stream or Run —
// before consuming the channel — to illustrate that the ID is available synchronously.
func RunIDLine(runID string) string {
	if runID == "" {
		return ""
	}
	return fmt.Sprintf("[run_id] %s", runID)
}

// PrintRunFooters prints usage and telemetry when SHOW_LLM_USAGE / SHOW_TELEMETRY are enabled.
func PrintRunFooters(result *agent.AgentRunResult) {
	if result == nil {
		return
	}
	if ShowLLMUsage() {
		if footer := LLMUsageFooter(result.LLMUsage); footer != "" {
			fmt.Println(footer)
		}
	}
	if ShowTelemetry() {
		if footer := TelemetryFooter(result.Telemetry); footer != "" {
			fmt.Println(footer)
		}
	}
}

func envBool(key string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "true")
}

// LLMUsageFooter returns a multi-line block describing token usage, or "".
func LLMUsageFooter(llmUsage *agent.LLMUsage) string {
	if llmUsage == nil {
		return ""
	}
	lines := []string{
		"\n[USAGE]",
		fmt.Sprintf("  prompt_tokens:     %d", llmUsage.PromptTokens),
		fmt.Sprintf("  completion_tokens: %d", llmUsage.CompletionTokens),
		fmt.Sprintf("  total_tokens:      %d", llmUsage.TotalTokens),
	}
	if llmUsage.CachedPromptTokens > 0 {
		lines = append(lines, fmt.Sprintf("  cached_prompt:     %d", llmUsage.CachedPromptTokens))
	}
	if llmUsage.ReasoningTokens > 0 {
		lines = append(lines, fmt.Sprintf("  reasoning_tokens:  %d", llmUsage.ReasoningTokens))
	}
	return strings.Join(lines, "\n")
}

// TelemetryFooter returns a multi-line block describing run telemetry, or "".
func TelemetryFooter(telemetry *agent.AgentTelemetry) string {
	if telemetry == nil {
		return ""
	}
	lines := []string{
		"[TELEMETRY RUN]",
		fmt.Sprintf("  total_llm_calls: %d", telemetry.Run.TotalLLMCalls),
		fmt.Sprintf("  started_at:      %s", formatTelemetryTime(telemetry.Run.StartedAt)),
		fmt.Sprintf("  completed_at:    %s", formatTelemetryTime(telemetry.Run.CompletedAt)),
	}
	if telemetry.Run.FinishReason != "" {
		lines = append(lines, fmt.Sprintf("  finish_reason:   %s", telemetry.Run.FinishReason))
	}

	lines = append(lines,
		"[TELEMETRY TOOLS]",
		fmt.Sprintf("  total_calls:  %d", telemetry.Tools.TotalCalls),
		fmt.Sprintf("  failed_calls: %d", telemetry.Tools.FailedCalls),
	)
	if len(telemetry.Tools.Breakdown) > 0 {
		lines = append(lines, "  breakdown:")
		for _, name := range sortedKeys(telemetry.Tools.Breakdown) {
			lines = append(lines, fmt.Sprintf("    %s: %d", name, telemetry.Tools.Breakdown[name]))
		}
	}
	if len(telemetry.Tools.FailedBreakdown) > 0 {
		lines = append(lines, "  failed_breakdown:")
		for _, name := range sortedKeys(telemetry.Tools.FailedBreakdown) {
			lines = append(lines, fmt.Sprintf("    %s: %d", name, telemetry.Tools.FailedBreakdown[name]))
		}
	}
	lines = append(lines,
		"[TELEMETRY STORAGE]",
		fmt.Sprintf("  total_retriever_searches:  %d", telemetry.Storage.TotalRetrieverSearches),
		fmt.Sprintf("  failed_retriever_searches: %d", telemetry.Storage.FailedRetrieverSearches),
		fmt.Sprintf("  prefetch_searches:         %d", telemetry.Storage.PrefetchSearches),
		fmt.Sprintf("  agentic_searches:          %d", telemetry.Storage.AgenticSearches),
		fmt.Sprintf("  total_memory_recalls:      %d", telemetry.Storage.TotalMemoryRecalls),
		fmt.Sprintf("  failed_memory_recalls:     %d", telemetry.Storage.FailedMemoryRecalls),
		fmt.Sprintf("  total_memory_stores:       %d", telemetry.Storage.TotalMemoryStores),
		fmt.Sprintf("  failed_memory_stores:      %d", telemetry.Storage.FailedMemoryStores),
	)
	return strings.Join(lines, "\n")
}

func formatTelemetryTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
