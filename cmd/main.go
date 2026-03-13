package main

import (
	"bufio"
	"context"
	"flag"
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
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/currenttime"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/echo"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/random"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/search"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/weather"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/wikipedia"
)

const exitPrompt = "Type 'exit', 'quit', or 'bye' to end the conversation."

func main() {
	configPath := flag.String("config", "", "path to config file (optional; uses env if empty)")
	flag.Parse()

	var cfg *config.Config
	if *configPath != "" {
		fc, err := loadConfigFromFile(*configPath)
		if err != nil {
			log.Fatalf("failed to load config: %v", err)
		}
		cfg = fileConfigToConfig(fc)
	} else {
		cfg = config.LoadFromEnv()
	}

	llmClient := newLLMClient(&llm.LLMConfig{
		Type:    cfg.LLM.Type,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		BaseURL: cfg.LLM.BaseURL,
	})

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
		agent.WithLogLevel(cfg.Log.Level),
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

func fileConfigToConfig(fc *fileConfig) *config.Config {
	cfg := &config.Config{
		Temporal: &config.TemporalConfig{
			Host:      fc.Temporal.Host,
			Port:      fc.Temporal.Port,
			Namespace: fc.Temporal.Namespace,
			TaskQueue: fc.Temporal.TaskQueue,
		},
		LLM: &config.LLMConfig{
			Type:    llm.LLMType(fc.LLM.Type),
			APIKey:  fc.LLM.APIKey,
			Model:   fc.LLM.Model,
			BaseURL: fc.LLM.BaseURL,
		},
	}
	cfg.Log = &config.LogConfig{Level: "error"}
	return cfg
}

func newLLMClient(cfg *llm.LLMConfig) interfaces.LLMClient {
	switch cfg.Type {
	case llm.LLMTypeAnthropic:
		return anthropic.NewClient(cfg)
	default:
		return openai.NewClient(cfg)
	}
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
