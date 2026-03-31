// agent_with_temporal_client demonstrates using WithTemporalClient to pass a pre-configured
// Temporal client to the agent. The caller owns the client lifecycle: create it, pass to the
// agent, and close it when done. Use this pattern when you need TLS, API key auth, Temporal
// Cloud, or other connection options not supported by WithTemporalConfig.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"go.temporal.io/sdk/client"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	// Create Temporal client ourselves. For local dev we use simple Dial; for production
	// you can add TLS, client.NewAPIKeyStaticCredentials(), ConnectionOptions, etc.
	hostPort := cfg.Host + ":" + strconv.Itoa(cfg.Port)
	tc, err := client.Dial(client.Options{
		HostPort:  hostPort,
		Namespace: cfg.Namespace,
		Logger:    config.NewLoggerFromLogConfig(cfg),
	})
	if err != nil {
		log.Fatalf("failed to create Temporal client: %v", err)
	}
	defer tc.Close()

	a, err := agent.NewAgent(
		agent.WithName("temporal-client-agent"),
		agent.WithDescription("Agent using caller-owned Temporal client"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalClient(tc),
		agent.WithTaskQueue(cfg.TaskQueue),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Hello, what can you do?"
	}
	fmt.Println("user:", prompt)
	response, err := a.Run(context.Background(), prompt, "")
	if err != nil {
		log.Printf("agent run failed: %v", err)
		return
	}
	fmt.Println("assistant:", response.Content)
}
