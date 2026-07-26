// agent_with_workflows demonstrates deterministic workflow execution inside an agent.
//
// The goal: the LLM routes user intent to a named procedure; the procedure runs
// step-by-step in an orchestration engine (validate → execute → return result).
// Steps, retries, and ordering are defined by the engine — not improvised by the LLM.
//
// workflow_tool.go    — WorkflowTool and WorkflowRunner interface (engine-agnostic)
// inprocess_runner.go — default runner (no infrastructure required)
// temporal_runner.go  — sample Temporal runner; activate with ORCHESTRATION_ENGINE=temporal
//
// Run (in-process, no infra needed):
//
//	go run ./agent_with_workflows "Run the onboarding workflow for Alice"
//
// Run (Temporal engine):
//
//	task infra:temporal:up && task infra:temporal:wait
//	ORCHESTRATION_ENGINE=temporal go run ./agent_with_workflows "Run the onboarding workflow for Alice"
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

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

	runner, stop, err := newRunner(cfg)
	if err != nil {
		log.Fatalf("failed to create workflow runner: %v", err)
	}
	defer stop()

	opts := []agent.Option{
		agent.WithName("agent-with-workflows"),
		agent.WithDescription("Agent that invokes deterministic user workflows via a custom tool"),
		agent.WithSystemPrompt(
			"You are a helpful operations assistant. " +
				"When the user asks to run a named workflow (onboarding, provisioning, status checks), " +
				"use the run_workflow tool with the matching workflow_name and input.",
		),
		agent.WithLLMClient(llmClient),
		agent.WithTools(NewWorkflowTool(runner)),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),

		// Workflows can be long-running: autonomous mode and an explicit tool ceiling.
		// MaxAttempts: 1 — do not blindly re-trigger an already-started workflow.
		// See docs/advanced/deterministic-execution.mdx for timeout sizing guidance.
		agent.WithAgentMode(agent.AgentModeAutonomous),
		agent.WithTimeout(10 * time.Minute),
		agent.WithToolExecutionConfig(agent.ExecutionConfig{
			Timeout:     10 * time.Minute,
			MaxAttempts: 1,
		}),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Run the onboarding workflow for user Alice"
	}

	fmt.Println("user:", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("agent run failed: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		return
	}
	fmt.Println("assistant:", result.Content)
	shared.PrintRunFooters(result)
}

// newRunner selects the WorkflowRunner based on the ORCHESTRATION_ENGINE env var.
//   - unset / "inprocess": no infrastructure needed
//   - "temporal":          requires ORCHESTRATION_ENGINE=temporal + Temporal server running
func newRunner(cfg *config.Config) (WorkflowRunner, func(), error) {
	switch strings.ToLower(os.Getenv("ORCHESTRATION_ENGINE")) {
	case "temporal":
		runner, stop, err := NewTemporalRunner(cfg, temporalLogAdapter(cfg))
		return runner, stop, err
	default:
		return newInProcessRunner(), func() {}, nil
	}
}
