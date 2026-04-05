package types

// LLMSampling holds per-agent LLM sampling overrides. nil/0 = provider default.
// One LLM client can serve multiple agents with different sampling.
type LLMSampling struct {
	Temperature *float64 // 0-2 OpenAI, 0-1 Anthropic
	MaxTokens   int      // 0 = provider default
	TopP        *float64 // 0-1; OpenAI only
	TopK        *int     // Anthropic only
}
