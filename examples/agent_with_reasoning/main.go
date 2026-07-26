package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
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
//   - Anthropic: extended thinking when BudgetTokens ≥ 1024 (or Enabled with default 1024); stream may show reasoning message deltas.
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
		agent.WithLLMClient(llmClient),
		agent.WithLLMSampling(&agent.LLMSampling{
			MaxTokens: 4096,
			Reasoning: &interfaces.LLMReasoning{
				Enabled:      true,
				BudgetTokens: 2048,
			},
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

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

	agentStream, err := a.Stream(context.Background(), prompt, nil)
	if err != nil {
		log.Fatalf("Stream: %v", err)
	}
	runID := agentStream.ID()
	eventCh, err := agentStream.Events(context.Background())
	if err != nil {
		log.Fatalf("stream events: %v", err)
	}
	fmt.Println(shared.RunIDLine(runID))
	fmt.Println("--- stream (REASONING_MESSAGE_CONTENT may appear before assistant text) ---")

	streamed := false
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		if shared.MarksStreamDelta(ev) {
			streamed = true
		}
		printEvent(ev, streamed)
	}
	fmt.Println()
}

func printEvent(ev agent.AgentEvent, streamedSoFar bool) {
	if ev == nil {
		return
	}
	switch ev.Type() {
	case agent.AgentEventTypeReasoningStart:
		fmt.Printf("\n[%s]\n", ev.Type())
	case agent.AgentEventTypeReasoningMessageStart:
		fmt.Printf("\n[%s]\n", ev.Type())
	case agent.AgentEventTypeReasoningMessageContent:
		if r, ok := ev.(*agent.AgentReasoningMessageContentEvent); ok && r.Delta != "" {
			fmt.Print(r.Delta)
		}
	case agent.AgentEventTypeReasoningMessageEnd:
		fmt.Printf("\n[%s]\n", ev.Type())
	case agent.AgentEventTypeReasoningEnd:
		fmt.Printf("\n[%s]\n", ev.Type())
	case agent.AgentEventTypeTextMessageContent:
		if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
			fmt.Print(t.Delta)
		}
	case agent.AgentEventTypeRunError:
		if re, ok := ev.(*agent.AgentRunErrorEvent); ok {
			fmt.Printf("\n[error] %s\n", re.Message)
		}
	case agent.AgentEventTypeRunFinished:
		res := shared.RunResultFromFinishedEvent(ev)
		if res != nil && res.Content != "" && !streamedSoFar {
			fmt.Printf("\n[complete] %s\n", res.Content)
		}
		shared.PrintRunFooters(res)
	default:
		// Ignore tool events (none registered).
	}
}
