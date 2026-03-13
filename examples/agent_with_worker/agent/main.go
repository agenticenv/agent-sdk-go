package main

import (
	"context"
	"fmt"
	"log"

	config "github.com/vinodvanja/temporal-agents-go/examples"
	"github.com/vinodvanja/temporal-agents-go/pkg/agent"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm/anthropic"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm/openai"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient := newLLMClient(&llm.LLMConfig{
		Type:    cfg.LLM.Type,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		BaseURL: cfg.LLM.BaseURL,
	})

	opts := []agent.Option{
		agent.WithName("agent"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.DisableWorker(),
		agent.WithLogLevel(cfg.Log.Level),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	response, err := a.Run(context.Background(), "Hello from remote agent!")
	if err != nil {
		log.Fatalf("failed to run agent: %v", err)
	}
	fmt.Println(response.Content)
}

func newLLMClient(cfg *llm.LLMConfig) interfaces.LLMClient {
	switch cfg.Type {
	case llm.LLMTypeAnthropic:
		return anthropic.NewClient(cfg)
	default:
		return openai.NewClient(cfg)
	}
}
