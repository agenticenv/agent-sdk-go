package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("simple-agent"),
		agent.WithDescription("Simple agent with built-in worker"),
		agent.WithSystemPrompt("You are a helpful assistant that can generate text."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Hello, what can you do?"
	}
	fmt.Println("user:", prompt)
	response, err := a.Run(context.Background(), prompt, "")
	if err != nil {
		log.Printf("agent foreground run failed: %v", err)
		return
	}
	fmt.Printf("assistant: %s\n", response.Content)
	if u := response.Usage; u != nil {
		fmt.Printf("\nusage: prompt=%d completion=%d total=%d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
		if u.CachedPromptTokens > 0 {
			fmt.Printf(" cached_prompt=%d", u.CachedPromptTokens)
		}
		if u.ReasoningTokens > 0 {
			fmt.Printf(" reasoning=%d", u.ReasoningTokens)
		}
		fmt.Println()
	}
}
