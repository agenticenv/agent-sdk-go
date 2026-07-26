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
		agent.WithName("simple-agent"),
		agent.WithDescription("Simple agent with built-in worker"),
		agent.WithSystemPrompt("You are a helpful assistant that can generate text."),
		agent.WithLLMClient(llmClient),
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
		prompt = "Hi"
	}
	fmt.Println("user:", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("agent foreground run failed: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		return
	}
	fmt.Printf("assistant: %s\n", result.Content)
	shared.PrintRunFooters(result)
}
