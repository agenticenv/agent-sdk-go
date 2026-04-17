package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	mcpclient "github.com/agenticenv/agent-sdk-go/pkg/mcp/client"
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

	mcpLogger := config.NewLoggerFromLogConfig(cfg)
	mcpOpts := []mcpclient.Option{
		mcpclient.WithLogger(mcpLogger),
		mcpclient.WithTimeout(cfg.MCPTimeout()),
		mcpclient.WithToolFilter(toolFilter),
	}
	if cfg.MCP.RetryAttempts > 0 {
		mcpOpts = append(mcpOpts, mcpclient.WithRetryAttempts(cfg.MCP.RetryAttempts))
	}

	mcpClient, err := mcpclient.NewClient(serverName, transport, mcpOpts...)
	if err != nil {
		log.Fatalf("failed to create MCP client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("agent-with-mcp-client"),
		agent.WithDescription("Agent with MCP from env: stdio or streamable HTTP (WithMCPClients)"),
		agent.WithSystemPrompt("You are a helpful assistant. Use MCP or other tools from your tool list when they help answer the user."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithMCPClients(mcpClient),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
		agent.WithLogLevel(cfg.LogLevel),
	}

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
	response, err := a.Run(context.Background(), prompt, "")
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	fmt.Println("assistant:", response.Content)
}
