package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/vinodvanja/temporal-agents-go/pkg/agent"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/calculator"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/currenttime"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/echo"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/random"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/search"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/weather"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/wikipedia"
)

const exitPrompt = "Type 'exit', 'quit', or 'bye' to end the conversation."

func main() {
	configPath := flag.String("config", "cmd/config.yaml", "path to config file (env overrides file values)")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	lgr := newLogger(cfg.Logger)
	llmClient, err := NewLLMClient(cfg, lgr)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	reg := tools.NewRegistry()
	reg.Register(echo.New())
	reg.Register(currenttime.New())
	reg.Register(random.New())
	reg.Register(calculator.New())
	reg.Register(weather.New())
	reg.Register(wikipedia.New())
	reg.Register(search.New())

	opts := []agent.Option{
		agent.WithName("agent"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithLogger(lgr),
		agent.WithApprovalHandler(approvalHandler),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Conversation mode. " + exitPrompt)
	fmt.Println()

	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if isExitCommand(line) {
			fmt.Println("Goodbye!")
			break
		}

		response, err := a.Run(context.Background(), line)
		if err != nil {
			log.Printf("agent error: %v", err)
			continue
		}
		fmt.Println("Agent:", response.Content)
		fmt.Println()
	}
}

func isExitCommand(s string) bool {
	switch strings.ToLower(s) {
	case "exit", "quit", "bye":
		return true
	}
	return false
}

func approvalHandler(ctx context.Context, req *agent.ApprovalRequest, onApproval agent.ApprovalSender) {
	argsJSON, _ := json.MarshalIndent(req.Args, "", "  ")
	fmt.Printf("\n--- Tool approval required ---\nTool: %s\nArgs:\n%s\nApprove? (y/n): ", req.ToolName, string(argsJSON))
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
