package main

import (
	"context"
	"fmt"
	"log"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg, echo.New()); err != nil {
		log.Fatalf("register tools: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("dynamic-registry"),
		agent.WithDescription("Agent whose tools can change between runs via ToolRegistry"),
		agent.WithSystemPrompt("You are a helpful assistant. Use tools when they are available; do not guess numeric results when a calculator tool exists."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	ctx := context.Background()
	mathPrompt := "What is 17 times 23? Use the calculator tool if you have it."

	fmt.Println("--- run 1 (echo only) ---")
	fmt.Println("user:", mathPrompt)
	agentRun1, err := a.Run(ctx, mathPrompt, nil)
	if err != nil {
		log.Printf("run 1 failed: %v", err)
	} else {
		result1, err := agentRun1.Get(ctx)
		if err != nil {
			log.Printf("run 1 get result: %v", err)
		} else {
			fmt.Println("agent:", result1.Content)
			shared.PrintRunFooters(result1)
		}
	}

	if err := a.ToolRegistry().Register(calculator.New()); err != nil {
		log.Fatalf("register calculator: %v", err)
	}
	fmt.Println("\nregistered calculator on ToolRegistry()")

	fmt.Println("\n--- run 2 (echo + calculator) ---")
	fmt.Println("user:", mathPrompt)
	agentRun2, err := a.Run(ctx, mathPrompt, nil)
	if err != nil {
		log.Printf("run 2 failed: %v", err)
		return
	}
	result2, err := agentRun2.Get(ctx)
	if err != nil {
		log.Printf("run 2 get result: %v", err)
		return
	}
	fmt.Println("agent:", result2.Content)
	shared.PrintRunFooters(result2)
}
