// Example demonstrating per-run budget enforcement with WithBudget.
//
// Two scenarios are shown:
//
//  1. BudgetStopRun: the run stops immediately and returns ErrBudgetExceeded when the
//     configured token or cost limit is reached.
//  2. BudgetWaitForApproval: the run pauses when the limit is reached; the approval
//     handler decides whether to continue or stop.
//
// Run:
//
//	go run ./agent_with_budget
//	go run ./agent_with_budget "Your custom prompt here"
//
// Set SHOW_LLM_USAGE=true to print token usage at the end of each run.
// The budget limits in this example are set very low so they trigger on most prompts.
// Raise them to realistic values (e.g. MaxTokens: 100_000) for production use.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Tell me a short interesting fact about space."
	}

	fmt.Println("=== Scenario 1: BudgetStopRun ===")
	runWithStopRun(cfg, llmClient, prompt)

	fmt.Println("\n=== Scenario 2: BudgetWaitForApproval ===")
	runWithApproval(cfg, llmClient, prompt)
}

// runWithStopRun shows a run that is stopped automatically when MaxTokens is reached.
func runWithStopRun(cfg *config.Config, llmClient interfaces.LLMClient, prompt string) {
	opts := []agent.Option{
		agent.WithName("budget-stop-agent"),
		agent.WithSystemPrompt("You are a helpful assistant. Be concise."),
		agent.WithLLMClient(llmClient),
		agent.WithBudget(agent.BudgetConfig{
			// Very low limit for demo purposes; raise for production.
			MaxTokens:  50,
			OnExceeded: agent.BudgetStopRun,
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	fmt.Printf("user: %s\n", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("run start error: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		if errors.Is(err, agent.ErrBudgetExceeded) {
			fmt.Println("run stopped: per-run budget exceeded (BudgetStopRun)")
		} else {
			log.Printf("run error: %v", err)
		}
		return
	}
	fmt.Printf("assistant: %s\n", result.Content)
	fmt.Printf("finish_reason: %s\n", result.Telemetry.Run.FinishReason)
	shared.PrintRunFooters(result)
}

// runWithApproval shows a run that pauses for human approval when MaxTokens is reached.
// In Run mode the registered ApprovalHandler is called synchronously.
// In Stream mode a CUSTOM "budget_approval" AG-UI event is emitted; call AgentStream.Approve
// with the token from the event payload to unblock the run.
func runWithApproval(cfg *config.Config, llmClient interfaces.LLMClient, prompt string) {
	autoApprove := strings.EqualFold(strings.TrimSpace(os.Getenv("EXAMPLES_AUTO_APPROVE")), "true")
	approvalHandler := func(_ context.Context, req *agent.ApprovalRequest) {
		v, err := agent.ParseBudgetApproval(req)
		if err != nil {
			_ = req.Respond(agent.ApprovalStatusRejected)
			return
		}
		fmt.Printf("[budget] limit reached: total_tokens=%d estimated_cost_usd=%.6f\n", v.TotalTokens, v.CostUSD)
		if autoApprove {
			fmt.Println("[budget] EXAMPLES_AUTO_APPROVE=true — continuing")
			if err := req.Respond(agent.ApprovalStatusApproved); err != nil {
				log.Printf("[budget] respond error: %v", err)
			}
			return
		}
		fmt.Print("[budget] continue this run? (y/n): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			log.Printf("[budget] read error: %v", err)
			_ = req.Respond(agent.ApprovalStatusRejected)
			return
		}
		status := agent.ApprovalStatusRejected
		if strings.TrimSpace(strings.ToLower(line)) == "y" {
			status = agent.ApprovalStatusApproved
		}
		if err := req.Respond(status); err != nil {
			log.Printf("[budget] respond error: %v", err)
		}
	}

	opts := []agent.Option{
		agent.WithName("budget-approval-agent"),
		agent.WithSystemPrompt("You are a helpful assistant. Be concise."),
		agent.WithLLMClient(llmClient),
		agent.WithApprovalHandler(approvalHandler),
		agent.WithBudget(agent.BudgetConfig{
			// Very low limits for demo purposes; raise for production.
			MaxTokens:          50,
			PromptUSDPer1M:     3.0,
			CompletionUSDPer1M: 15.0,
			MaxCostUSD:         0.001,
			OnExceeded:         agent.BudgetWaitForApproval,
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	fmt.Printf("user: %s\n", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("run start error: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		if errors.Is(err, agent.ErrBudgetExceeded) {
			fmt.Println("run stopped: budget exceeded and approval denied")
		} else {
			log.Printf("run error: %v", err)
		}
		return
	}
	fmt.Printf("assistant: %s\n", result.Content)
	fmt.Printf("finish_reason: %s\n", result.Telemetry.Run.FinishReason)
	shared.PrintRunFooters(result)
}
