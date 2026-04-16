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
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("agent-with-tool-authorizer"),
		agent.WithDescription("Agent with a custom tool that uses ToolAuthorizer"),
		agent.WithSystemPrompt("You are a helpful assistant. Use the protected_note tool when the user asks for the protected note or internal note."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithStream(true),
		agent.WithTools(NewProtectedNote()),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Get the protected note for roadmap."
	}

	fmt.Println("user:", prompt)
	fmt.Println("tip: set ALLOW_PROTECTED_NOTE=1 to authorize the tool")

	eventCh, err := a.Stream(context.Background(), prompt, "")
	if err != nil {
		log.Printf("stream failed: %v", err)
		return
	}

	fmt.Println("--- events ---")

	streamed := false
	var finalContent string
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		if ev.Type == agent.AgentEventContentDelta || ev.Type == agent.AgentEventContent {
			streamed = true
		}
		printEvent(ev, streamed)
		if ev.Type == agent.AgentEventComplete {
			finalContent = ev.Content
		}
	}
	if finalContent != "" {
		fmt.Println()
	}
}

func printEvent(ev *agent.AgentEvent, streamedContent bool) {
	switch ev.Type {
	case agent.AgentEventContent:
		if ev.Content != "" {
			fmt.Print(ev.Content)
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
			tc := ev.ToolCall
			if len(tc.Args) == 0 {
				fmt.Printf("\n[tool_call] %s\n", tc.ToolName)
			} else {
				args, _ := json.Marshal(tc.Args)
				fmt.Printf("\n[tool_call] %s args=%s\n", tc.ToolName, string(args))
			}
		}
	case agent.AgentEventToolResult:
		if ev.ToolCall != nil {
			fmt.Printf("[tool_result] %s (%s): %v\n", ev.ToolCall.ToolName, ev.ToolCall.Status, ev.ToolCall.Result)
		}
	case agent.AgentEventError:
		fmt.Printf("[error] %s\n", ev.Content)
	case agent.AgentEventComplete:
		// Only print content if we didn't already display it via ContentDelta or Content
		if ev.Content != "" && !streamedContent {
			who := strings.TrimSpace(ev.AgentName)
			if who == "" {
				who = "agent"
			}
			fmt.Printf("\n[%s complete] %s\n", who, ev.Content)
		}
	default:
		fmt.Printf("[%s] %+v\n", ev.Type, ev)
	}
}
