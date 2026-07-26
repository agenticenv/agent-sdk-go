// Package main demonstrates execution config.
//
// Each agent loop operation (LLM call, tool execute, MCP, sub-agent delegation, etc.)
// has its own independent timeout and retry budget. This example shows how to override
// those defaults using With*ExecutionConfig options.
//
// SDK defaults used when no override is set:
//
//	LLM          Timeout: 30m  MaxAttempts: 3
//	ToolAuth     Timeout: 30m  MaxAttempts: 1
//	ToolExecute  Timeout: 30m  MaxAttempts: 3
//	MCP          Timeout: 30m  MaxAttempts: 3
//	A2A          Timeout: 30m  MaxAttempts: 3
//	Retriever    Timeout: 5m   MaxAttempts: 3
//	Memory       Timeout: 5m   MaxAttempts: 3
//	Conversation Timeout: 30s  MaxAttempts: 1
//	SubAgent     Timeout: (agent run timeout)  MaxAttempts: 1
//
// Zero fields in ExecutionConfig keep the SDK default for that field.
//
// See: docs/features/execution-config.mdx
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
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/currenttime"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("exec-config-agent"),
		agent.WithDescription("Agent that demonstrates execution config"),
		agent.WithSystemPrompt(
			"You are a helpful assistant. Use the available tools when they help answer the question. " +
				"Give a concise final answer.",
		),
		agent.WithLLMClient(llmClient),
		agent.WithTools(calculator.New(), currenttime.New()),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),

		// LLM: tighter timeout for a chat agent; keep 3 retries to ride out transient errors.
		agent.WithLLMExecutionConfig(agent.ExecutionConfig{
			Timeout:     10 * time.Minute,
			MaxAttempts: 3,
		}),

		// ToolExecute: shorter deadline — tools should be fast; one attempt is usually enough.
		agent.WithToolExecutionConfig(agent.ExecutionConfig{
			Timeout:     2 * time.Minute,
			MaxAttempts: 1,
		}),

		// Conversation: keep SDK default by omitting the option entirely (shown here for reference;
		// zero fields also fall back to defaults, so WithConversationExecutionConfig({}) is a no-op).

		// SubAgent: explicit ceiling — delegates must finish within 5 minutes.
		// Without this the sub-agent timeout falls back to the agent run timeout (WithTimeout).
		agent.WithSubAgentExecutionConfig(agent.ExecutionConfig{
			Timeout: 5 * time.Minute,
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
		prompt = "What is 123 multiplied by 456? Also, what time is it right now?"
	}

	fmt.Println("user:", prompt)
	fmt.Println()

	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Fatalf("agent run failed: %v", err)
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		log.Fatalf("run get result: %v", err)
	}

	fmt.Printf("assistant: %s\n", result.Content)
	shared.PrintRunFooters(result)
}
