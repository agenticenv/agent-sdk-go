package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/currenttime"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/random"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/search"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/weather"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/wikipedia"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

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
		agent.WithDescription("Agent that streams events via Stream"),
		agent.WithSystemPrompt("You are a helpful assistant with access to tools. Use them when appropriate: current time, weather, math, random numbers, Wikipedia, and web search."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithStream(true),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What's the current time and what's 17 * 23?"
	}

	fmt.Println("user:", prompt)

	eventCh, err := a.Stream(context.Background(), prompt, "")
	if err != nil {
		log.Printf("Stream failed: %v", err)
		return
	}

	fmt.Println("--- events ---")

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
		// Final text is often duplicate when tokens were streamed; skip verbose line.
		if ev.Content != "" && !streamedSoFar {
			fmt.Printf("[complete] %s\n", ev.Content)
		} else if ev.Content != "" {
			fmt.Println("[complete]")
		}
		if ev.Usage != nil {
			u := ev.Usage
			fmt.Printf("\n[usage] prompt=%d completion=%d total=%d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
			if u.CachedPromptTokens > 0 {
				fmt.Printf(" cached_prompt=%d", u.CachedPromptTokens)
			}
			if u.ReasoningTokens > 0 {
				fmt.Printf(" reasoning=%d", u.ReasoningTokens)
			}
			fmt.Println()
		}
	default:
		fmt.Printf("[%s] %+v\n", ev.Type, ev)
	}
}
