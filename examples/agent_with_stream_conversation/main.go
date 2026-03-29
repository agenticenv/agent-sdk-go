package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/vvsynapse/agent-sdk-go/examples"
	"github.com/vvsynapse/agent-sdk-go/pkg/agent"
	"github.com/vvsynapse/agent-sdk-go/pkg/conversation/inmem"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools/calculator"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools/echo"
)

// agent_with_stream_conversation demonstrates RunStream with conversation and
// proper event handling: ContentDelta/Content streamed to user, Complete content
// not re-printed when already displayed (avoids duplicate output).
func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	conv := inmem.NewInMemoryConversation(inmem.WithMaxSize(100))

	reg := tools.NewRegistry()
	reg.Register(echo.New())
	reg.Register(calculator.New())

	opts := []agent.Option{
		agent.WithName("agent-stream-conversation"),
		agent.WithDescription("RunStream with conversation; shows event handling pattern to avoid duplicate output"),
		agent.WithSystemPrompt("You are a helpful assistant. Remember context. Use tools: echo, calculator."),
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
		agent.WithConversation(conv),
		agent.WithConversationSize(20),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	convID := "session-1"
	if id := os.Getenv("CONVERSATION_ID"); id != "" {
		convID = id
	}

	if len(os.Args) > 1 {
		prompt := strings.Join(os.Args[1:], " ")
		runSingleTurn(context.Background(), a, prompt, convID)
		return
	}

	runInteractive(context.Background(), a, convID)
}

func runSingleTurn(ctx context.Context, a *agent.Agent, prompt, convID string) {
	fmt.Println("user:", prompt)
	fmt.Print("assistant: ")
	eventCh, err := a.RunStream(ctx, prompt, convID)
	if err != nil {
		log.Printf("RunStream failed: %v", err)
		return
	}
	handleEvents(eventCh)
	fmt.Println()
}

func runInteractive(ctx context.Context, a *agent.Agent, convID string) {
	fmt.Println("Multi-turn conversation with streaming. Type 'exit' or 'quit' to end.")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\nuser: ")
		if !scanner.Scan() {
			break
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}
		if prompt == "exit" || prompt == "quit" || prompt == "bye" {
			break
		}
		eventCh, err := a.RunStream(ctx, prompt, convID)
		if err != nil {
			log.Printf("RunStream failed: %v", err)
			continue
		}
		fmt.Print("assistant: ")
		handleEvents(eventCh)
		fmt.Println()
	}
}

// handleEvents processes the event stream. Tracks streamedContent so we don't
// re-print Complete's content when it was already shown via ContentDelta/Content.
// Caller prints "assistant: " before calling so all events (tools, content) are under assistant.
func handleEvents(eventCh <-chan *agent.AgentEvent) {
	var streamedContent bool
	var finalContent string
	for ev := range eventCh {
		if ev == nil {
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
	_ = finalContent // use for logging/storage if needed
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
			fmt.Printf("\n[tool_call] %s (%s) args=%s\n", ev.ToolCall.ToolName, ev.ToolCall.ToolCallID, string(args))
		}
	case agent.AgentEventToolResult:
		if ev.ToolCall != nil {
			fmt.Printf("[tool_result] %s: %v\n", ev.ToolCall.ToolName, ev.ToolCall.Result)
		}
	case agent.AgentEventApproval:
		// Handled in main loop; Approval events are not printed here
	case agent.AgentEventError:
		fmt.Printf("[error] %s\n", ev.Content)
	case agent.AgentEventComplete:
		// Only print content if we didn't already display it via ContentDelta or Content
		if ev.Content != "" && !streamedContent {
			fmt.Print(ev.Content)
		}
	default:
		fmt.Printf("[%s] %+v\n", ev.Type, ev)
	}
}
