package types

import "github.com/agenticenv/agent-sdk-go/pkg/interfaces"

// LLMSampling holds per-agent LLM sampling overrides. nil/0 = provider default.
// One LLM client can serve multiple agents with different sampling.
type LLMSampling struct {
	Temperature *float64 // 0-2 OpenAI, 0-1 Anthropic; also Gemini
	MaxTokens   int      // 0 = provider default
	TopP        *float64 // 0-1; OpenAI and Gemini (not Anthropic)
	TopK        *int     // Anthropic only
	// Reasoning: optional generic reasoning/thinking (see interfaces.LLMReasoning); mapped per provider.
	Reasoning *interfaces.LLMReasoning
}
