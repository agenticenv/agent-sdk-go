package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/agent_with_worker/opts"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	// Common opts (name, description, system prompt, Temporal, LLM, logger)
	baseOpts := opts.Common(cfg.Host, cfg.Port, cfg.Namespace, cfg.TaskQueue, llmClient, config.NewLoggerFromLogConfig(cfg))
	// Agent-specific: no embedded worker, use remote workers, timeout for interactive use
	agentOpts := append(baseOpts,
		agent.DisableLocalWorker(),
		agent.EnableRemoteWorkers(),
		agent.WithTimeout(3*time.Minute),
	)

	a, err := agent.NewAgent(agentOpts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Hello, what can you do?"
	}
	fmt.Println("user:", prompt)
	fmt.Println("(Ensure worker is running in another terminal. Waits up to 15s for workers.)")
	response, err := a.Run(context.Background(), prompt, "")
	if err != nil {
		log.Printf("failed to run agent: %v", err)
		return
	}
	fmt.Println("assistant: ", response.Content)
}
