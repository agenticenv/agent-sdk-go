package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/vinodvanja/temporal-agents-go/examples"
	"github.com/vinodvanja/temporal-agents-go/pkg/agent"
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
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithTools(NewReverser(), NewWordCount()),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()), // allow all tools without approval (default requires approval)
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Reverse 'hello world' and tell me how many words it has."
	}

	fmt.Println("user:", prompt)
	response, err := a.Run(context.Background(), prompt)
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	fmt.Println("agent:", response.Content)
}
