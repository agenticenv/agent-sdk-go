package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation/inmem"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
)

// agent_with_stream_conversation demonstrates Stream with conversation and
// proper event handling: streamed text not duplicated from RUN_FINISHED.
func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	conv := inmem.NewConversation(inmem.WithMaxSize(100))

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		echo.New(),
		calculator.New(),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}
	opts := []agent.Option{
		agent.WithName("agent-stream-conversation"),
		agent.WithDescription("Stream with conversation; shows event handling pattern to avoid duplicate output"),
		agent.WithSystemPrompt("You are a helpful assistant. Remember context. Use tools: echo, calculator."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithConversation(conversation.DefaultConfig(conv)),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
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
	streamOpts := &agent.AgentStreamOptions{
		ConversationOptions: &agent.ConversationOptions{
			ID: convID,
		},
	}
	agentStream, err := a.Stream(ctx, prompt, streamOpts)
	if err != nil {
		log.Printf("Stream failed: %v", err)
		return
	}
	runID := agentStream.ID()
	eventCh, err := agentStream.Events(ctx)
	if err != nil {
		log.Printf("stream events: %v", err)
		return
	}
	fmt.Println(shared.RunIDLine(runID))
	fmt.Println("assistant:")
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
		streamOpts := &agent.AgentStreamOptions{
			ConversationOptions: &agent.ConversationOptions{
				ID: convID,
			},
		}
		agentStream, err := a.Stream(ctx, prompt, streamOpts)
		if err != nil {
			log.Printf("Stream failed: %v", err)
			continue
		}
		runID := agentStream.ID()
		eventCh, err := agentStream.Events(ctx)
		if err != nil {
			log.Printf("stream events: %v", err)
			continue
		}
		fmt.Println(shared.RunIDLine(runID))
		fmt.Print("assistant: ")
		handleEvents(eventCh)
		fmt.Println()
	}
}

// handleEvents processes the event stream. Tracks streamedContent so we don't
// re-print RUN_FINISHED body when it was already streamed as TEXT_MESSAGE_CONTENT.
func handleEvents(eventCh <-chan agent.AgentEvent) {
	var streamedContent bool
	var finalContent string
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		if shared.MarksStreamDelta(ev) {
			streamedContent = true
		}
		printEvent(ev, streamedContent)
		if res := shared.RunResultFromFinishedEvent(ev); res != nil && res.Content != "" {
			finalContent = res.Content
		}
	}
	_ = finalContent
}

func printEvent(ev agent.AgentEvent, streamedContent bool) {
	if ev == nil {
		return
	}
	switch eventType := ev.Type(); eventType {
	case agent.AgentEventTypeTextMessageContent:
		if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
			fmt.Print(t.Delta)
		}
	case agent.AgentEventTypeReasoningMessageContent:
		if r, ok := ev.(*agent.AgentReasoningMessageContentEvent); ok && r.Delta != "" {
			fmt.Printf("\n[thinking] %s", r.Delta)
		}
	case agent.AgentEventTypeToolCallStart:
		if t, ok := ev.(*agent.AgentToolCallStartEvent); ok {
			fmt.Printf("\n[%s] %s (%s)\n", eventType, t.ToolCallName, t.ToolCallID)
		}
	case agent.AgentEventTypeToolCallArgs:
		if t, ok := ev.(*agent.AgentToolCallArgsEvent); ok && t.Delta != "" {
			fmt.Printf("\n[%s] %s %s\n", eventType, t.ToolCallID, t.Delta)
		}
	case agent.AgentEventTypeToolCallResult:
		if t, ok := ev.(*agent.AgentToolCallResultEvent); ok {
			fmt.Printf("[%s] %s: %s\n", eventType, t.ToolCallID, t.Content)
		}
	case agent.AgentEventTypeRunStarted:
		if r, ok := ev.(*agent.AgentRunStartedEvent); ok {
			fmt.Printf("\n[%s] threadID=%s runID=%s\n", eventType, r.ThreadID, r.RunID)
		}
	case agent.AgentEventTypeRunError:
		if re, ok := ev.(*agent.AgentRunErrorEvent); ok {
			fmt.Printf("\n[%s] %s\n", eventType, re.Message)
		}
	case agent.AgentEventTypeRunFinished:
		res := shared.RunResultFromFinishedEvent(ev)
		if res == nil {
			return
		}
		if res.Content != "" && !streamedContent {
			fmt.Printf("\n[%s] %s\n", eventType, res.Content)
		} else {
			fmt.Printf("\n[%s]\n", eventType)
		}
		shared.PrintRunFooters(res)
	default:
		//fmt.Printf("[%s] %+v\n", eventType, ev)
		return
	}
}
