package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// Demonstrates generic [interfaces.LLMReasoning] via [agent.WithLLMSampling].
//
// How to run (from repo root, with Temporal up and examples/.env configured):
//
//	go run ./examples/agent_with_reasoning/
//	go run ./examples/agent_with_reasoning/ "Why is the sky blue? Answer in one short paragraph."
//
// What to expect by provider:
//   - OpenAI: reasoning_effort is sent only when Effort is non-empty; omit Effort for standard chat models.
//     For o1/o3/gpt-5-style reasoning models, set Effort e.g. "low" or "medium" in WithLLMSampling.
//   - Anthropic: extended thinking when BudgetTokens ≥ 1024 (or Enabled with default 1024); stream may show thinking_delta tokens.
//   - Gemini: ThinkingConfig from Enabled / Effort / BudgetTokens; thought parts may appear in the model output depending on model support.
//
// Use a reasoning-capable / extended-thinking model in LLM_MODEL for best results.
func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("agent-with-reasoning"),
		agent.WithDescription("Example: WithLLMSampling + generic LLMReasoning"),
		agent.WithSystemPrompt("You are a helpful assistant. Be concise."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithStream(true),
		agent.WithLLMSampling(&agent.LLMSampling{
			MaxTokens: 4096,
			Reasoning: &interfaces.LLMReasoning{
				Enabled:      true,
				BudgetTokens: 2048,
				// Effort: set e.g. "medium" for OpenAI reasoning models only; leave unset for gpt-4o etc.
			},
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 17 × 23? Show brief reasoning, then the number."
	}

	fmt.Println("user:", prompt)
	fmt.Println("--- stream (thinking_delta appears for Anthropic extended thinking; content follows) ---")

	eventCh, err := a.Stream(context.Background(), prompt, "")
	if err != nil {
		log.Fatalf("Stream: %v", err)
	}

	streamed := false
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		if ev.Type == agent.AgentEventContentDelta || ev.Type == agent.AgentEventThinkingDelta {
			streamed = true
		}
		printEvent(ev, streamed)
	}
	fmt.Println()
}

func printEvent(ev *agent.AgentEvent, streamedSoFar bool) {
	switch ev.Type {
	case agent.AgentEventContentDelta:
		if ev.Content != "" {
			fmt.Print(ev.Content)
		}
	case agent.AgentEventThinkingDelta:
		if ev.Content != "" {
			fmt.Print(ev.Content)
		}
	case agent.AgentEventThinking:
		if ev.Content != "" {
			fmt.Printf("\n[thinking] %s\n", ev.Content)
		}
	case agent.AgentEventError:
		fmt.Printf("\n[error] %s\n", ev.Content)
	case agent.AgentEventComplete:
		if ev.Content != "" && !streamedSoFar {
			fmt.Printf("\n[complete] %s\n", ev.Content)
		}
	default:
		// Ignore tool events (none registered).
	}
}
