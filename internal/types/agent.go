package types

// AgentResponse is the structured result of a completed run (content, model, metadata).
type AgentResponse struct {
	Content   string         `json:"content"`
	AgentName string         `json:"agent_name"`
	Model     string         `json:"model"`
	Metadata  map[string]any `json:"metadata"`
}

// RunAsyncResult is the single outcome from RunAsync. After the channel closes, Err is non-nil
// on failure; otherwise Response is non-nil.
type RunAsyncResult struct {
	Response *AgentResponse
	Err      error
}
