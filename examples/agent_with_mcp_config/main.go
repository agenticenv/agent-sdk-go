package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()
	transport, err := config.MCPLoadTransport(cfg)
	if err != nil {
		log.Fatal(err)
	}
	toolFilter, err := config.MCPToolFilterFromConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	serverName := config.MCPDefaultServerName(cfg)

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	mcpCfg := agent.MCPConfig{
		Transport:  transport,
		ToolFilter: toolFilter,
	}

	if d := cfg.MCPTimeout(); d > 0 {
		mcpCfg.Timeout = d
	}
	if cfg.MCP.RetryAttempts > 0 {
		mcpCfg.RetryAttempts = cfg.MCP.RetryAttempts
	}

	opts := []agent.Option{
		agent.WithName("agent-with-mcp-config"),
		agent.WithDescription("Agent with MCP from env: stdio or streamable HTTP (WithMCPConfig)"),
		agent.WithSystemPrompt("You are a helpful assistant. Use MCP or other tools from your tool list when they help answer the user."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithMCPConfig(agent.MCPServers{serverName: mcpCfg}),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
		agent.WithLogLevel(cfg.LogLevel),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What tools do you have available?"
	}

	fmt.Println("user:", prompt)
	response, err := a.Run(context.Background(), prompt, "")
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	fmt.Println("assistant:", response.Content)
}
