// Example demonstrating input and output guardrails.
//
// RegexRedact removes US Social Security numbers from the prompt and from the
// model response. MaxLength rejects an oversized prompt. moderate is a stand-in
// for your own client: it denies a prompt that contains MODERATION_BLOCK.
//
// Run:
//
//	go run ./agent_with_guardrails
//	go run ./agent_with_guardrails "My SSN is 123-45-6789. What is the return policy?"
//
// A prompt containing MODERATION_BLOCK ends with agent.ErrGuardrailBlocked.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/guardrail"
)

func main() {
	cfg := config.LoadFromEnv()
	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	redact, err := guardrail.RegexRedact(
		"ssn",
		`\b\d{3}-\d{2}-\d{4}\b`,
		"[ssn]",
		guardrail.StageInput,
		guardrail.StageOutput,
	)
	if err != nil {
		log.Fatal(err)
	}
	limit, err := guardrail.MaxLength("prompt-length", 4000, guardrail.StageInput)
	if err != nil {
		log.Fatal(err)
	}

	opts := []agent.Option{
		agent.WithName("guardrail-agent"),
		agent.WithSystemPrompt("You are a helpful assistant. Be concise."),
		agent.WithLLMClient(llmClient),
		agent.WithGuardrail(redact, limit),
		agent.WithGuardrail(guardrail.New("moderation", moderate, guardrail.StageInput)),
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
		prompt = "My SSN is 123-45-6789. What is a good return policy for a small shop?"
	}

	fmt.Printf("user: %s\n", prompt)
	run, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Fatal(err)
	}
	result, err := run.Get(context.Background())
	if err != nil {
		if errors.Is(err, agent.ErrGuardrailBlocked) {
			log.Fatalf("blocked: %v", err)
		}
		log.Fatal(err)
	}
	fmt.Printf("assistant: %s\n", result.Content)
}

// moderate is the sample you replace with a call to your own moderation client.
func moderate(_ context.Context, in guardrail.Input) (guardrail.Decision, error) {
	if strings.Contains(in.Text, "MODERATION_BLOCK") {
		return guardrail.Deny("moderation"), nil
	}
	return guardrail.Allow(), nil
}
