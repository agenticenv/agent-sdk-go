package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/vinodvanja/temporal-agents-go/examples"
	"github.com/vinodvanja/temporal-agents-go/pkg/agent"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm/anthropic"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm/openai"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/calculator"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/currenttime"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/echo"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/random"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/search"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/weather"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/wikipedia"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient := newLLMClient(&llm.LLMConfig{
		Type:    cfg.LLM.Type,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		BaseURL: cfg.LLM.BaseURL,
	})

	reg := tools.NewRegistry()
	reg.Register(echo.New())
	reg.Register(currenttime.New())
	reg.Register(random.New())
	reg.Register(calculator.New())
	reg.Register(weather.New())
	reg.Register(wikipedia.New())
	reg.Register(search.New())

	opts := []agent.Option{
		agent.WithName("agent-with-tools"),
		agent.WithDescription("Agent with echo, currenttime, random, calculator, weather, wikipedia, search tools"),
		agent.WithSystemPrompt("You are a helpful assistant with access to tools. Use them when appropriate: current time, weather, math, random numbers, Wikipedia, and web search."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()), // allow all tools without approval (default requires approval)
		agent.WithLogLevel(cfg.Log.Level),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What's the current time and what's 17 * 23?"
	}

	fmt.Println("user:", prompt)
	response, err := a.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}
	fmt.Println("agent:", response.Content)
}

func newLLMClient(cfg *llm.LLMConfig) interfaces.LLMClient {
	switch cfg.Type {
	case llm.LLMTypeAnthropic:
		return anthropic.NewClient(cfg)
	default:
		return openai.NewClient(cfg)
	}
}
