package types

// AgentRunOptions holds per-call options passed to [Agent.Run].
// A nil pointer is valid and means "no options" (no conversation, default behaviour).
// Add new per-call knobs here as nested option structs; keep agent-level settings on [agentConfig].
type AgentRunOptions struct {
	// ConversationOptions selects a conversation session for this call.
	// Required when the agent was configured with WithConversation; must be nil otherwise.
	ConversationOptions *ConversationOptions `json:"conversation_options,omitempty"`
}

// AgentStreamOptions holds per-call options passed to [Agent.Stream].
// A nil pointer is valid and means "no options" (LLM token streaming on, no conversation).
// Add new streaming-specific knobs here; keep agent-level settings on [agentConfig].
type AgentStreamOptions struct {
	// ConversationOptions selects a conversation session for this streaming call.
	// Required when the agent was configured with WithConversation; must be nil otherwise.
	ConversationOptions *ConversationOptions `json:"conversation_options,omitempty"`

	// DisableTokenStreaming, when true, instructs the LLM to produce a single complete
	// response rather than a token-by-token stream. All other AG-UI events (lifecycle,
	// tool calls, CUSTOM events) are still emitted. Default false = stream tokens.
	DisableTokenStreaming bool `json:"disable_token_streaming,omitempty"`
}

// ConversationOptions identifies a conversation session for one call.
// ID must be a non-empty, stable string that is the same across all turns of a session
// (e.g. a user or chat ID). The agent loads history for this ID before the LLM call
// and persists the new messages after it completes.
type ConversationOptions struct {
	ID string
}

// AgentRunResult is the structured result of a completed run (content, model, metadata).
type AgentRunResult struct {
	Content   string         `json:"content"`
	AgentName string         `json:"agent_name"`
	Model     string         `json:"model"`
	Metadata  map[string]any `json:"metadata"`
	// RunID identifies this run for correlation and (on Temporal) GetAgentRun / GetAgentStream.
	// Populated for completed [Agent.Run] results; for live streams prefer the
	// runID returned synchronously from [Agent.Stream] / [AgentRun.ID] / [AgentStream.ID].
	RunID string `json:"run_id,omitempty"`
	// Usage is the sum of token usage across all LLM calls in this run (when reported by the provider).
	// Usage acts as the historical root for aggregated token counters.
	LLMUsage *LLMUsage `json:"llm_usage,omitempty"`

	// Telemetry contains the strongly typed nested metrics domain payload.
	Telemetry *AgentTelemetry `json:"telemetry,omitempty"`
}

// AgentMode distinguishes how the agent is driven: human-in-the-loop versus self-directed runs.
// The string value is stable for configuration and fingerprints (see pkg/agent.WithAgentMode).
type AgentMode string

const (
	// AgentModeInteractive is the default: the agent expects user turns, approvals, or other
	// interactive signals between steps when the product requires them.
	AgentModeInteractive AgentMode = "interactive"
	// AgentModeAutonomous indicates a run where the agent proceeds without blocking on user input
	// for each step (subject to tool policy and limits).
	AgentModeAutonomous AgentMode = "autonomous"
)

// ToolExecutionMode specifies how tools are executed in parallel or sequentially.
type AgentToolExecutionMode string

const (
	// AgentToolExecutionModeParallel specifies that tools are executed in parallel.
	AgentToolExecutionModeParallel AgentToolExecutionMode = "parallel"
	// AgentToolExecutionModeSequential specifies that tools are executed sequentially.
	AgentToolExecutionModeSequential AgentToolExecutionMode = "sequential"
)

type RunStatus string

const (
	StatusPending   RunStatus = "pending"   // Scheduled or queued
	StatusRunning   RunStatus = "running"   // Actively executing
	StatusCompleted RunStatus = "completed" // Finished successfully
	StatusFailed    RunStatus = "failed"    // Encountered an error
	StatusCancelled RunStatus = "cancelled" // Stopped via context/Cancel()
)

// Helper methods attached to the RunStatus type itself
func (s RunStatus) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

func (s RunStatus) IsCancelled() bool {
	return s == StatusCancelled
}

func (s RunStatus) String() string {
	return string(s)
}
