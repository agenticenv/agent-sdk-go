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

	// Custom tools defined in this example (reverser.go, wordcount.go)
	// Using WithTools for ad-hoc tool registration
	opts := []agent.Option{
		agent.WithName("agent-with-custom-tools"),
		agent.WithDescription("Agent with custom reverser and word_count tools"),
		agent.WithSystemPrompt("You are a helpful assistant. You can reverse text or count words. Use the tools when the user asks for those operations."),
		agent.WithLLMClient(llmClient),
		agent.WithTools(NewReverser(), NewWordCount()),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()), // allow all tools without approval (default requires approval)
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
		prompt = "Reverse 'hello world' and tell me how many words it has."
	}

	fmt.Println("user:", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		return
	}
	fmt.Println("agent:", result.Content)
	shared.PrintRunFooters(result)
}
