package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/vinodvanja/temporal-agents-go/examples"
	"github.com/vinodvanja/temporal-agents-go/pkg/agent"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm/anthropic"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm/openai"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/calculator"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/echo"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient := newLLMClient(&llm.LLMConfig{
		Type:    cfg.LLM.Type,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		BaseURL: cfg.LLM.BaseURL,
	})

	reg := tools.NewRegistry()
	reg.Register(echo.New())
	reg.Register(calculator.New())

	opts := []agent.Option{
		agent.WithName("agent-with-tools-approval"),
		agent.WithDescription("Agent with tools that require user approval before execution"),
		agent.WithSystemPrompt("You are a helpful assistant. Use the echo or calculator tool when asked."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		// Default policy: all tools require approval (no WithToolApprovalPolicy)
		agent.WithApprovalHandler(approvalHandler),
		agent.WithLogLevel(cfg.Log.Level),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 17 + 23?"
	}

	fmt.Println("user:", prompt)
	response, err := a.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}
	fmt.Println("agent:", response.Content)
}

func approvalHandler(ctx context.Context, req *agent.ApprovalRequest, onApproval agent.ApprovalSender) {
	fmt.Printf("\n--- Tool approval required ---\nTool: %s\nArgs: %v\nApprove? (y/n): ", req.ToolName, req.Args)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		fmt.Println("no input")
		return
	}
	if strings.TrimSpace(strings.ToLower(scanner.Text())) == "y" {
		onApproval(agent.ApprovalStatusApproved)
	} else {
		onApproval(agent.ApprovalStatusRejected)
	}
}

func newLLMClient(cfg *llm.LLMConfig) interfaces.LLMClient {
	switch cfg.Type {
	case llm.LLMTypeAnthropic:
		return anthropic.NewClient(cfg)
	default:
		return openai.NewClient(cfg)
	}
}
