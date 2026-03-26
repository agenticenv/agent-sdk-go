package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/vvsynapse/temporal-agent-sdk-go/examples"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/agent"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/tools"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/tools/calculator"
)

// This example demonstrates that tool approval events from a sub-agent (MathSpecialist)
// flow up to the main agent's RunStream subscriber on the same in-memory channel.
//
// Approval flow:
//  1. Main agent asks to delegate to MathSpecialist → approval prompt (kind: delegation)
//  2. MathSpecialist calls the calculator tool    → approval prompt (kind: tool, from sub-agent)
//
// Both approvals arrive on the main agent's RunStream event channel, proving that
// sub-agent events fan-in to the root agent's LocalChannelName.
func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	baseQueue := cfg.TaskQueue
	mathQueue := baseQueue + "-math-specialist"
	mainQueue := baseQueue + "-main-agent"

	mathReg := tools.NewRegistry()
	mathReg.Register(calculator.New())

	// MathSpecialist uses RequireAllToolApprovalPolicy so its calculator tool
	// also requires approval — we observe this approval on the main agent's stream.
	mathSpecialist, err := agent.NewAgent(
		agent.WithName("MathSpecialist"),
		agent.WithDescription("Arithmetic specialist with calculator tool."),
		agent.WithSystemPrompt("You are a math specialist. Use the calculator tool for arithmetic. Reply with a short final answer."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: mathQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(mathReg),
		agent.WithToolApprovalPolicy(agent.RequireAllToolApprovalPolicy{}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	)
	if err != nil {
		log.Fatalf("math specialist agent: %v", err)
	}
	defer mathSpecialist.Close()

	mainAgent, err := agent.NewAgent(
		agent.WithName("Main agent"),
		agent.WithDescription("General assistant."),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: mainQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithSubAgents(mathSpecialist),
		agent.WithMaxSubAgentDepth(2),
		agent.WithStream(true),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	)
	if err != nil {
		log.Fatalf("main agent: %v", err)
	}
	defer mainAgent.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 987 multiplied by 654?"
	}

	fmt.Println("user:", prompt)
	fmt.Println("All approvals (main agent delegation + sub-agent calculator) are handled here.")
	fmt.Println()

	eventCh, err := mainAgent.RunStream(context.Background(), prompt, "")
	if err != nil {
		log.Fatalf("run stream failed: %v", err)
	}

	for ev := range eventCh {
		switch ev.Type {
		case agent.AgentEventToolApproval:
			ap := ev.Approval
			if ap == nil {
				continue
			}
			argsJSON, _ := json.MarshalIndent(ap.Args, "", "  ")
			title := "Tool approval"
			if ap.Kind == agent.ToolApprovalKindDelegation {
				title = "Delegate to specialist"
			}
			fmt.Printf("\n--- %s ---\n", title)
			fmt.Printf("Source agent : %s\n", ap.AgentName)
			if ap.DelegateToName != "" {
				fmt.Printf("Delegate to  : %s\n", ap.DelegateToName)
			}
			fmt.Printf("Tool         : %s\n", ap.ToolName)
			fmt.Printf("Args:\n%s\nApprove? (y/n): ", string(argsJSON))

			approved := false
			select {
			case line, ok := <-lineCh:
				approved = ok && strings.TrimSpace(strings.ToLower(line)) == "y"
			}
			status := agent.ApprovalStatusRejected
			if approved {
				status = agent.ApprovalStatusApproved
			}
			if err := mainAgent.OnApproval(context.Background(), ap.ApprovalToken, status); err != nil {
				fmt.Printf("approval error: %v\n", err)
			}

		case agent.AgentEventContentDelta:
			fmt.Print(ev.Content)

		case agent.AgentEventComplete:
			fmt.Printf("\nmain agent: %s\n", ev.Content)
		}
	}
}
