package types

import "time"

// AgentTelemetry is the unified container for operational insights across
// a single agent run, run lifecycle, tool calls, and storage operations.
type AgentTelemetry struct {
	// Run captures the orchestration lifecycle metrics for the run.
	Run RunTelemetry `json:"run"`

	// Tools tracks tool invocation counts and breakdowns for the run.
	Tools ToolTelemetry `json:"tools"`

	// Storage tracks data storage and retrieval operations for the run.
	Storage StorageTelemetry `json:"storage"`
}

type FinishReason string

const (
	// FinishReasonComplete indicates that the agent run completed normally.
	FinishReasonComplete FinishReason = "complete"
	// FinishReasonMaxIterations indicates that the agent run completed because the maximum number of iterations was reached.
	FinishReasonMaxIterations FinishReason = "max_iterations"
	// FinishReasonBudgetExceeded indicates that the agent run stopped because a per-run budget
	// limit (MaxTokens or MaxCostUSD) was reached and the configured action was BudgetStopRun,
	// or because the caller denied continuation after BudgetWaitForApproval.
	FinishReasonBudgetExceeded FinishReason = "budget_exceeded"
)

// RunTelemetry captures the orchestration lifecycle metrics for a single agent run.
type RunTelemetry struct {
	// StartedAt tracks the start time of the agent run.
	StartedAt time.Time `json:"started_at"`

	// CompletedAt tracks the completion time of the agent run.
	CompletedAt time.Time `json:"completed_at"`

	// TotalLLMCalls counts how many LLM calls were made during the run.
	// Each iteration counts as one call regardless of how many Temporal retries it took.
	TotalLLMCalls int64 `json:"total_llm_calls"`

	// LLMRetryCount is the total number of Temporal activity retries across all LLM calls in
	// this run. A value of 0 means every LLM activity succeeded on its first attempt.
	// Temporal also exposes per-activity retry counts natively in its metrics and workflow history.
	LLMRetryCount int64 `json:"llm_retry_count,omitempty"`

	// FinishReason explains how the run concluded. See FinishReason for possible values.
	FinishReason FinishReason `json:"finish_reason"`
}

// ToolTelemetry tracks tool invocation counts and per-tool breakdowns across a single agent run.
type ToolTelemetry struct {
	// TotalCalls is the total number of tool invocations made by the agent.
	TotalCalls int64 `json:"total_calls"`

	// FailedCalls is the number of tool invocations that returned an error.
	// Excludes approval-denied and unauthorized cases.
	FailedCalls int64 `json:"failed_calls"`

	// Breakdown tracks invocation counts per tool name.
	// Key: tool name (e.g. "palo_alto_fw_lookup"), Value: invocation count
	Breakdown map[string]int64 `json:"breakdown,omitempty"`

	// FailedBreakdown tracks failed invocation counts per tool name.
	// Excludes approval-denied and unauthorized cases.
	// Key: tool name (e.g. "palo_alto_fw_lookup"), Value: failed invocation count
	FailedBreakdown map[string]int64 `json:"failed_breakdown,omitempty"`
}

// Record increments tool invocation counters for name. When failed is true, failed counters are updated too.
// Breakdown keys are omitted when name is empty. Caller must serialize concurrent Record calls (e.g. mutex in parallel tool execution).
func (t *ToolTelemetry) Record(name string, failed bool) {
	if t == nil {
		return
	}
	t.TotalCalls++
	if t.Breakdown == nil {
		t.Breakdown = make(map[string]int64)
	}
	if name != "" {
		t.Breakdown[name]++
	}
	if !failed {
		return
	}
	t.FailedCalls++
	if t.FailedBreakdown == nil {
		t.FailedBreakdown = make(map[string]int64)
	}
	if name != "" {
		t.FailedBreakdown[name]++
	}
}

// StorageTelemetry tracks RAG retrieval operations across prefetch, agentic, and hybrid modes.
// All fields are zero when no retriever is configured.
type StorageTelemetry struct {
	// Retriever — RAG searches (prefetch/agentic/hybrid), zero if not configured.
	TotalRetrieverSearches  int64 `json:"total_retriever_searches"`
	FailedRetrieverSearches int64 `json:"failed_retriever_searches"`

	// Breakdown by mode — zero if mode not used.
	PrefetchSearches int64 `json:"prefetch_searches,omitempty"`
	AgenticSearches  int64 `json:"agentic_searches,omitempty"`

	// Memory — long-term recall/store operations, zero if not configured.
	TotalMemoryRecalls  int64 `json:"total_memory_recalls,omitempty"`
	FailedMemoryRecalls int64 `json:"failed_memory_recalls,omitempty"`
	TotalMemoryStores   int64 `json:"total_memory_stores,omitempty"`
	FailedMemoryStores  int64 `json:"failed_memory_stores,omitempty"`
}
