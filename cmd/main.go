package main

import (
	"context"
	"flag"
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
	configPath := flag.String("config", "", "path to config file (optional; uses env if empty)")
	flag.Parse()

	var cfg *config.Config
	if *configPath != "" {
		fc, err := loadConfigFromFile(*configPath)
		if err != nil {
			log.Fatalf("failed to load config: %v", err)
		}
		cfg = fileConfigToConfig(fc)
	} else {
		cfg = config.LoadFromEnv()
	}

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
		agent.WithLogLevel(cfg.Log.Level),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	response, err := a.Run(context.Background(), "Hello, world!")
	if err != nil {
		log.Fatalf("failed to run agent: %v", err)
	}
	fmt.Println(response.Content)
}

func fileConfigToConfig(fc *fileConfig) *config.Config {
	cfg := &config.Config{
		Temporal: &config.TemporalConfig{
			Host:      fc.Temporal.Host,
			Port:      fc.Temporal.Port,
			Namespace: fc.Temporal.Namespace,
			TaskQueue: fc.Temporal.TaskQueue,
		},
		LLM: &config.LLMConfig{
			Type:    llm.LLMType(fc.LLM.Type),
			APIKey:  fc.LLM.APIKey,
			Model:   fc.LLM.Model,
			BaseURL: fc.LLM.BaseURL,
		},
	}
	cfg.Log = &config.LogConfig{Level: "error"}
	return cfg
}

func newLLMClient(cfg *llm.LLMConfig) interfaces.LLMClient {
	switch cfg.Type {
	case llm.LLMTypeAnthropic:
		return anthropic.NewClient(cfg)
	default:
		return openai.NewClient(cfg)
	}
}
