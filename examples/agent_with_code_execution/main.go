// agent_with_code_execution demonstrates sandboxed code execution inside an agent.
//
// The goal: the LLM writes code to answer a user question; the code runs in an
// isolated sandbox and returns the output. The LLM reads the output and replies.
// The sandbox is pluggable — swap the runtime without changing the tool contract.
//
// code_tool.go    — CodeTool and SandboxRuntime interface (sandbox-agnostic)
// local_runner.go — default runtime using os/exec (needs Python or Node on host)
// docker_runner.go — isolated runtime using Docker; activate with SANDBOX_ENV=docker
//
// Run (local sandbox — needs Python or Node installed):
//
//	go run ./agent_with_code_execution "Write a Python script that prints the first 10 Fibonacci numbers"
//
// Run (Docker sandbox — needs Docker daemon):
//
//	SANDBOX_ENV=docker go run ./agent_with_code_execution "Write a Python script that prints the first 10 Fibonacci numbers"
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

	// Use newLocalRunner by default (no infra required).
	// Set SANDBOX_ENV=docker for isolated execution in a Docker container.
	sandbox, err := newSandbox()
	if err != nil {
		log.Fatalf("failed to create sandbox: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("agent-with-code-execution"),
		agent.WithDescription("Agent that writes and executes code in a sandboxed environment"),
		agent.WithSystemPrompt(
			"You are a helpful coding assistant with access to a code execution sandbox. " +
				"When the user asks you to compute something, verify logic, or run code, " +
				"write a short self-contained script and call execute_code. " +
				"Read the output and explain the result to the user. " +
				"Do not make up output — always run the code first.",
		),
		agent.WithLLMClient(llmClient),
		agent.WithTools(NewCodeTool(sandbox)),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),

		// Code execution is typically fast; a 2-minute ceiling covers slow Docker pulls
		// or heavy computation. See docs/advanced/code-execution.mdx for sizing guidance.
		agent.WithAgentMode(agent.AgentModeAutonomous),
		agent.WithToolExecutionConfig(agent.ExecutionConfig{
			Timeout:     2 * time.Minute,
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
		prompt = "Write a Python script that prints the first 10 Fibonacci numbers"
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

// newSandbox selects the SandboxRuntime based on the SANDBOX_ENV env var.
//   - unset / "local": os/exec on the host — needs Python or Node installed
//   - "docker":        Docker container — needs SANDBOX_ENV=docker + Docker daemon
func newSandbox() (SandboxRuntime, error) {
	switch strings.ToLower(os.Getenv("SANDBOX_ENV")) {
	case "docker":
		return newDockerRunner()
	default:
		return newLocalRunner(), nil
	}
}
