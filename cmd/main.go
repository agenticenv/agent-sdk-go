package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation/inmem"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/currenttime"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/random"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/search"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/weather"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/wikipedia"
)

const (
	version    = "1.0.0"
	exitPrompt = "Type 'exit', 'quit', or 'bye' to end the conversation."
	convID     = "interactive"
)

func main() {
	var configPath string
	var showVersion bool
	flag.StringVar(&configPath, "config", "cmd/config.yaml", "path to config file (env overrides file values)")
	flag.StringVar(&configPath, "c", "cmd/config.yaml", "alias for -config")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&showVersion, "v", false, "alias for -version")
	flag.Parse()

	if showVersion {
		fmt.Printf("%s\n", version)
		return
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	lgr := newLogger(cfg.Logger)
	llmClient, err := NewLLMClient(cfg, lgr)
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

	conv := inmem.NewInMemoryConversation(inmem.WithMaxSize(100))

	// Single stdin reader: avoids conflict between main loop and approval handler after timeout
	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	opts := []agent.Option{
		agent.WithName("agentctl"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithStream(true),
		agent.WithToolRegistry(reg),
		agent.WithConversation(conv),
		agent.WithConversationSize(20),
		agent.WithLogger(lgr),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	fmt.Println("Conversation mode. " + exitPrompt)

	for {
		fmt.Print("\nYou: ")
		line, ok := <-lineCh
		if !ok {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if isExitCommand(line) {
			fmt.Println("Goodbye!")
			break
		}

		eventCh, err := a.RunStream(context.Background(), line, convID)
		if err != nil {
			log.Printf("agent error: %v", err)
			continue
		}
		fmt.Print("assistant: ")
		var finalContent string
		var streamedContent bool
		for ev := range eventCh {
			if ev == nil {
				continue
			}
			if ev.Type == agent.AgentEventApproval && ev.Approval != nil {
				argsJSON, _ := json.MarshalIndent(ev.Approval.Args, "", "  ")
				fmt.Printf("\n--- Tool approval required ---\nTool: %s\nArgs:\n%s\nApprove? (y/n): ", ev.Approval.ToolName, string(argsJSON))
				line, ok := <-lineCh
				status := agent.ApprovalStatusRejected
				if ok && strings.TrimSpace(strings.ToLower(line)) == "y" {
					status = agent.ApprovalStatusApproved
				}
				if err := a.OnApproval(context.Background(), ev.Approval.ApprovalToken, status); err != nil {
					log.Printf("approval failed: %v", err)
				}
				continue
			}
			if ev.Type == agent.AgentEventContentDelta || ev.Type == agent.AgentEventContent {
				streamedContent = true
			}
			printEvent(ev, streamedContent)
			if ev.Type == agent.AgentEventComplete {
				finalContent = ev.Content
			}
		}
		if finalContent != "" {
			fmt.Println()
		}
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
			args, _ := json.Marshal(ev.ToolCall.Args)
			fmt.Printf("\n[tool_call] %s args=%s\n", ev.ToolCall.ToolName, string(args))
		}
	case agent.AgentEventApproval:
		// Handled in main loop; Approval events are not printed here
	case agent.AgentEventToolResult:
		if ev.ToolCall != nil {
			fmt.Printf("[tool_result] %s: %v\n", ev.ToolCall.ToolName, ev.ToolCall.Result)
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

func isExitCommand(s string) bool {
	switch strings.ToLower(s) {
	case "exit", "quit", "bye":
		return true
	}
	return false
}
