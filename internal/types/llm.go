package types

// LLMUsage reports token counts from the provider for one completion. Values are best-effort:
// some fields may be zero when the API does not return them.
type LLMUsage struct {
	PromptTokens       int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens   int64 `json:"completion_tokens,omitempty"`
	TotalTokens        int64 `json:"total_tokens,omitempty"`
	CachedPromptTokens int64 `json:"cached_prompt_tokens,omitempty"`
	ReasoningTokens    int64 `json:"reasoning_tokens,omitempty"`
}

// LLMReasoning configures reasoning/thinking in a provider-agnostic way.
// Each LLM client maps these fields to its API; fields that do not apply are ignored.
type LLMReasoning struct {
	// Enabled requests reasoning/thinking where the provider supports it.
	// Anthropic: if true and BudgetTokens is 0, uses the minimum extended-thinking budget (1024 tokens).
	// OpenAI: does not infer reasoning_effort from Enabled alone (standard models reject that param).
	// Gemini: contributes to turning on thought output with IncludeThoughts.
	Enabled bool

	// Effort is a generic reasoning intensity: "none", "minimal", "low", "medium", "high", "xhigh".
	// OpenAI: sent as reasoning_effort only when non-empty; use only with reasoning-capable models.
	// Gemini: mapped to ThinkingLevel when recognized (low/medium/high/minimal), unless BudgetTokens > 0.
	// Anthropic: not used (use Enabled and BudgetTokens for extended thinking).
	Effort string

	// BudgetTokens is the token budget for internal reasoning / extended thinking.
	// Anthropic: extended thinking; must be >= 1024 when non-zero (values below are clamped).
	// Gemini: ThinkingBudget. If non-zero, Effort is not mapped to ThinkingLevel (API allows only one).
	// OpenAI: not used.
	BudgetTokens int
}

// LLMSampling holds per-agent LLM sampling overrides. nil/0 = provider default.
// One LLM client can serve multiple agents with different sampling.
type LLMSampling struct {
	Temperature *float64 // 0-2 OpenAI, 0-1 Anthropic; also Gemini
	MaxTokens   int      // 0 = provider default
	TopP        *float64 // 0-1; OpenAI and Gemini (not Anthropic)
	TopK        *int     // Anthropic only
	// Reasoning: optional generic reasoning/thinking; mapped per provider.
	Reasoning *LLMReasoning
}
