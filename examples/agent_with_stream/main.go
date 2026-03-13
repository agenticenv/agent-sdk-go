package main

import (
	"context"
	"encoding/json"
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
		agent.WithName("agent-with-stream"),
		agent.WithDescription("Agent that streams events via RunStream"),
		agent.WithSystemPrompt("You are a helpful assistant with access to tools. Use them when appropriate: current time, weather, math, random numbers, Wikipedia, and web search."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithStream(true),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
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
	fmt.Println("--- events ---")

	eventCh, err := a.RunStream(context.Background(), prompt)
	if err != nil {
		log.Fatalf("RunStream failed: %v", err)
	}

	var finalContent string
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		printEvent(ev)
		if ev.Type == agent.AgentEventComplete {
			finalContent = ev.Content
		}
	}

	fmt.Println("--- final response ---")
	fmt.Println(finalContent)
}

func printEvent(ev *agent.AgentEvent) {
	switch ev.Type {
	case agent.AgentEventContent:
		if ev.Content != "" {
			fmt.Printf("[content] %s\n", ev.Content)
		}
	case agent.AgentEventContentDelta:
		if ev.Content != "" {
			fmt.Print(ev.Content)
		}
	case agent.AgentEventThinking:
		if ev.Content != "" {
			fmt.Printf("[thinking] %s\n", ev.Content)
		}
	case agent.AgentEventThinkingDelta:
		if ev.Content != "" {
			fmt.Print(ev.Content)
		}
	case agent.AgentEventToolCall:
		if ev.ToolCall != nil {
			args, _ := json.Marshal(ev.ToolCall.Args)
			fmt.Printf("[tool_call] %s (%s) args=%s\n", ev.ToolCall.ToolName, ev.ToolCall.ToolCallID, string(args))
		}
	case agent.AgentEventToolResult:
		if ev.ToolCall != nil {
			fmt.Printf("[tool_result] %s: %v\n", ev.ToolCall.ToolName, ev.ToolCall.Result)
		}
	case agent.AgentEventError:
		fmt.Printf("[error] %s\n", ev.Content)
	case agent.AgentEventComplete:
		fmt.Printf("[complete] %s\n", ev.Content)
	default:
		fmt.Printf("[%s] %+v\n", ev.Type, ev)
	}
}

func newLLMClient(cfg *llm.LLMConfig) interfaces.LLMClient {
	switch cfg.Type {
	case llm.LLMTypeAnthropic:
		return anthropic.NewClient(cfg)
	default:
		return openai.NewClient(cfg)
	}
}
