package main

import (
	"bufio"
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
)

// This example demonstrates that tool approval events from a sub-agent (MathSpecialist)
// flow up to the main agent's Stream subscriber on the same in-memory channel.
//
// Approval flow:
//  1. Main agent asks to delegate to MathSpecialist → CUSTOM name=delegation
//  2. MathSpecialist calls the calculator tool    → CUSTOM name=approval
//
// Both approvals arrive on the main agent's Stream event channel, proving that
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

	mathReg := agent.NewToolRegistry()
	if err := mathReg.Register(calculator.New()); err != nil {
		log.Fatalf("register tools: %v", err)
	}

	mathAgentOpts := []agent.Option{
		agent.WithName("MathSpecialist"),
		agent.WithDescription("Arithmetic specialist with calculator tool."),
		agent.WithSystemPrompt("You are a math specialist. Use the calculator tool for arithmetic. Reply with a short final answer."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(mathReg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	if cfg.UseTemporalRuntime() {
		mathAgentOpts = append(mathAgentOpts, agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: mathQueue,
		}))
	}
	mathSpecialist, err := agent.NewAgent(mathAgentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("math specialist agent", err))
	}
	defer mathSpecialist.Close()

	mainAgentOpts := []agent.Option{
		agent.WithName("Main agent"),
		agent.WithDescription("General assistant."),
		agent.WithSystemPrompt(
			"You are the main assistant. For arithmetic, delegate using the MathSpecialist sub-agent tool. " +
				"When the specialist's answer comes back, do not stop there: continue as the main agent—give the user a concise final reply that includes the result, " +
				"then add one short sentence of your own (e.g. sanity check, related tip, or offer to help further). " +
				"Always produce visible assistant text after delegation completes.",
		),
		agent.WithLLMClient(llmClient),
		agent.WithSubAgents(mathSpecialist),
		agent.WithMaxSubAgentDepth(2),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	mainAgentOpts = append(mainAgentOpts, config.ToolApprovalOptions()...)
	if cfg.UseTemporalRuntime() {
		mainAgentOpts = append(mainAgentOpts, agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: mainQueue,
		}))
	}
	mainAgent, err := agent.NewAgent(mainAgentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("main agent", err))
	}
	defer mainAgent.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 987 multiplied by 654? After you get the exact value, say in one sentence whether that order of magnitude is typical for a quick mental estimate."
	}

	fmt.Println("user:", prompt)
	fmt.Println("All approvals (main agent delegation + sub-agent calculator) are handled here.")
	fmt.Println()

	agentStream, err := mainAgent.Stream(context.Background(), prompt, nil)
	if err != nil {
		log.Fatalf("run stream failed: %v", err)
	}
	runID := agentStream.ID()
	eventCh, err := agentStream.Events(context.Background())
	if err != nil {
		log.Fatalf("stream events: %v", err)
	}
	fmt.Println(shared.RunIDLine(runID))

	for ev := range eventCh {
		if ev == nil {
			continue
		}
		switch eventType := ev.Type(); eventType {
		case agent.AgentEventTypeStepStarted:
			if t, ok := ev.(*agent.AgentStepStartedEvent); ok && t.StepName != "" {
				fmt.Printf("[%s] %s\n", eventType, t.StepName)
			}
		case agent.AgentEventTypeStepFinished:
			if t, ok := ev.(*agent.AgentStepFinishedEvent); ok && t.StepName != "" {
				fmt.Printf("[%s] %s\n", eventType, t.StepName)
			}
		case agent.AgentEventTypeCustom:
			if tv, ok := shared.ToolApprovalValueFromEvent(ev); ok {
				argsJSON, _ := json.MarshalIndent(tv.Args, "", "  ")
				fmt.Printf("\n--- Tool approval ---\n")
				fmt.Printf("[%s] Source agent : %s\n", eventType, tv.AgentName)
				fmt.Printf("[%s] Tool         : %s\n", eventType, tv.ToolName)
				fmt.Printf("[%s] Args:\n%s\nApprove? (y/n): ", eventType, string(argsJSON))

				line, ok := <-lineCh
				approved := ok && strings.TrimSpace(strings.ToLower(line)) == "y"
				status := agent.ApprovalStatusRejected
				if approved {
					status = agent.ApprovalStatusApproved
				}
				if err := agentStream.Approve(context.Background(), tv.ApprovalToken, status); err != nil {
					fmt.Printf("[%s] approval error: %v\n", eventType, err)
				}
				continue
			}
			if dv, ok := shared.DelegationApprovalValueFromEvent(ev); ok {
				argsJSON, _ := json.MarshalIndent(dv.Args, "", "  ")
				fmt.Printf("\n--- Delegate to specialist ---\n")
				fmt.Printf("[%s] Source agent : %s\n", eventType, dv.AgentName)
				fmt.Printf("[%s] Delegate to  : %s\n", eventType, dv.SubAgentName)
				fmt.Printf("[%s] Args:\n%s\nApprove? (y/n): ", eventType, string(argsJSON))

				line, ok := <-lineCh
				approved := ok && strings.TrimSpace(strings.ToLower(line)) == "y"
				status := agent.ApprovalStatusRejected
				if approved {
					status = agent.ApprovalStatusApproved
				}
				if err := agentStream.Approve(context.Background(), dv.ApprovalToken, status); err != nil {
					fmt.Printf("[%s] approval error: %v\n", eventType, err)
				}
			}

		case agent.AgentEventTypeTextMessageContent:
			if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
				fmt.Printf("[%s] %s\n", eventType, t.Delta)
			}

		case agent.AgentEventTypeRunFinished:
			res := shared.RunResultFromFinishedEvent(ev)
			if res == nil {
				continue
			}
			who := strings.TrimSpace(res.AgentName)
			if who == "" {
				who = "agent"
			}
			fmt.Printf("\n[%s] [%s complete] %s\n", eventType, who, res.Content)
			shared.PrintRunFooters(res)
		}
	}
}
