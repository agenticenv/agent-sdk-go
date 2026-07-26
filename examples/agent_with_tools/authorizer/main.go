package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
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
		agent.WithLLMClient(llmClient),
		agent.WithTools(NewProtectedNote()),
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
		prompt = "Get the protected note for roadmap."
	}

	fmt.Println("user:", prompt)
	fmt.Println("tip: set ALLOW_PROTECTED_NOTE=1 to authorize the tool")

	agentStream, err := a.Stream(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("stream failed: %v", err)
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
	var finalContent string
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		if shared.MarksStreamDelta(ev) {
			streamed = true
		}
		printEvent(ev, streamed)
		if res := shared.RunResultFromFinishedEvent(ev); res != nil && res.Content != "" {
			finalContent = res.Content
		}
	}
	if finalContent != "" {
		fmt.Println()
	}
}

func printEvent(ev agent.AgentEvent, streamedContent bool) {
	if ev == nil {
		return
	}
	switch ev.Type() {
	case agent.AgentEventTypeTextMessageContent:
		if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
			fmt.Print(t.Delta)
		}
	case agent.AgentEventTypeReasoningMessageContent:
		if r, ok := ev.(*agent.AgentReasoningMessageContentEvent); ok && r.Delta != "" {
			fmt.Printf("[thinking] %s", r.Delta)
		}
	case agent.AgentEventTypeToolCallStart:
		if t, ok := ev.(*agent.AgentToolCallStartEvent); ok {
			fmt.Printf("\n[tool_call] %s\n", t.ToolCallName)
		}
	case agent.AgentEventTypeToolCallArgs:
		if t, ok := ev.(*agent.AgentToolCallArgsEvent); ok && t.Delta != "" {
			fmt.Printf("[tool_args] %s\n", t.Delta)
		}
	case agent.AgentEventTypeToolCallResult:
		if t, ok := ev.(*agent.AgentToolCallResultEvent); ok {
			fmt.Printf("[tool_result] %s: %s\n", t.ToolCallID, t.Content)
		}
	case agent.AgentEventTypeRunError:
		if re, ok := ev.(*agent.AgentRunErrorEvent); ok {
			fmt.Printf("[error] %s\n", re.Message)
		}
	case agent.AgentEventTypeRunFinished:
		res := shared.RunResultFromFinishedEvent(ev)
		if res == nil || res.Content == "" {
			return
		}
		if !streamedContent {
			who := strings.TrimSpace(res.AgentName)
			if who == "" {
				who = "agent"
			}
			fmt.Printf("\n[%s complete] %s\n", who, res.Content)
		}
		shared.PrintRunFooters(res)
	default:
		//fmt.Printf("[%s] %+v\n", ev.Type(), ev)
		return
	}
}
