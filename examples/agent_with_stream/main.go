package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"

	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
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

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		echo.New(),
		currenttime.New(),
		random.New(),
		calculator.New(),
		weather.New(),
		wikipedia.New(),
		search.New(),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}
	opts := []agent.Option{
		agent.WithName("agent-with-stream"),
		agent.WithDescription("Agent that streams events via Stream"),
		agent.WithSystemPrompt("You are a helpful assistant with access to tools. Use them when appropriate: current time, weather, math, random numbers, Wikipedia, and web search."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
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
		prompt = "What's the current time?"
	}

	fmt.Println("user:", prompt)

	// runID is available synchronously — before any event arrives on the channel.
	// Persist it before consuming eventCh when crash-durability or reconnect matters.
	agentStream, err := a.Stream(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("Stream failed: %v", err)
		return
	}
	runID := agentStream.ID()
	eventCh, err := agentStream.Events(context.Background())
	if err != nil {
		log.Printf("stream events: %v", err)
		return
	}
	fmt.Println(shared.RunIDLine(runID))
	fmt.Println("--- events ---")

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
	switch eventType := ev.Type(); eventType {
	case agent.AgentEventTypeTextMessageStart:
		if t, ok := ev.(*agent.AgentTextMessageStartEvent); ok {
			fmt.Printf("\n[%s] %s\n", eventType, t.MessageID)
		}
	case agent.AgentEventTypeTextMessageContent:
		if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
			fmt.Print(t.Delta)
		}
	case agent.AgentEventTypeTextMessageEnd:
		if t, ok := ev.(*agent.AgentTextMessageEndEvent); ok {
			fmt.Printf("\n[%s] %s\n", eventType, t.MessageID)
		}
	case agent.AgentEventTypeToolCallStart:
		if t, ok := ev.(*agent.AgentToolCallStartEvent); ok {
			fmt.Printf("\n[%s] %s (%s)\n", eventType, t.ToolCallName, t.ToolCallID)
		}
	case agent.AgentEventTypeToolCallArgs:
		if t, ok := ev.(*agent.AgentToolCallArgsEvent); ok && t.Delta != "" {
			var args any
			if json.Unmarshal([]byte(t.Delta), &args) == nil {
				b, _ := json.Marshal(args)
				fmt.Printf("[%s] %s args=%s\n", eventType, t.ToolCallID, string(b))
			} else {
				fmt.Printf("[%s] %s raw=%s\n", eventType, t.ToolCallID, t.Delta)
			}
		}
	case agent.AgentEventTypeToolCallEnd:
		if t, ok := ev.(*agent.AgentToolCallEndEvent); ok {
			fmt.Printf("[%s] %s\n", eventType, t.ToolCallID)
		}
	case agent.AgentEventTypeToolCallResult:
		if t, ok := ev.(*agent.AgentToolCallResultEvent); ok {
			fmt.Printf("[%s] %s: %s\n", eventType, t.ToolCallID, t.Content)
		}
	case agent.AgentEventTypeRunError:
		if re, ok := ev.(*agent.AgentRunErrorEvent); ok {
			fmt.Printf("[%s] %s\n", eventType, re.Message)
		}
	case agent.AgentEventTypeRunStarted:
		if r, ok := ev.(*agent.AgentRunStartedEvent); ok {
			fmt.Printf("[%s] threadID=%s runID=%s\n", eventType, r.ThreadID, r.RunID)
		}
	case agent.AgentEventTypeRunFinished:
		res := shared.RunResultFromFinishedEvent(ev)
		if res != nil && res.Content != "" && !streamedSoFar {
			fmt.Printf("[%s] %s\n", eventType, res.Content)
		} else if res != nil && res.Content != "" {
			fmt.Printf("[%s]", eventType)
		}
		shared.PrintRunFooters(res)
	default:
		//fmt.Printf("[%s] %+v\n", ev.Type(), ev)
		return
	}
}
