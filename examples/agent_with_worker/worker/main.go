package main

import (
	"log"
	"os"
	"os/signal"

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
		agent.WithName("agent-worker"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithLogLevel(cfg.Log.Level),
	}

	w, err := agent.NewAgentWorker(opts...)
	if err != nil {
		log.Fatalf("failed to create agent worker: %v", err)
	}
	defer w.Close()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	go func() {
		if err := w.Start(); err != nil {
			log.Printf("worker stopped: %v", err)
		}
	}()

	<-sigChan
	log.Println("shutting down...")
}

func newLLMClient(cfg *llm.LLMConfig) interfaces.LLMClient {
	switch cfg.Type {
	case llm.LLMTypeAnthropic:
		return anthropic.NewClient(cfg)
	default:
		return openai.NewClient(cfg)
	}
}
