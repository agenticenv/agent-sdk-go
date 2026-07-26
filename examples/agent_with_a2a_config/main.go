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
	a2aCfg, err := config.A2ABuildAgentConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	serverName := config.A2ADefaultServerName(cfg)

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("agent-with-a2a-config"),
		agent.WithDescription("Agent with A2A from env (WithA2AConfig)"),
		agent.WithSystemPrompt("You are a helpful assistant. Use A2A tools from your tool list when they help answer the user."),
		agent.WithLLMClient(llmClient),
		agent.WithA2AConfig(agent.A2AServers{serverName: a2aCfg}),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
		agent.WithLogLevel(cfg.LogLevel),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What tools do you have available?"
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
	fmt.Println("assistant:", result.Content)
	shared.PrintRunFooters(result)
}
