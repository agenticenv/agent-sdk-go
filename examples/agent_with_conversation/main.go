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
	"github.com/vinodvanja/temporal-agents-go/pkg/conversation/inmem"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/calculator"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools/echo"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	// In-memory conversation for multi-turn context (single process only).
	conv := inmem.NewInMemoryConversation(inmem.WithMaxSize(100))

	reg := tools.NewRegistry()
	reg.Register(echo.New())
	reg.Register(calculator.New())

	opts := []agent.Option{
		agent.WithName("agent-with-conversation"),
		agent.WithDescription("Agent with in-memory conversation and tools for multi-turn context"),
		agent.WithSystemPrompt("You are a helpful assistant. Remember the conversation context. Use tools when helpful: echo for repeating, calculator for math."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithConversation(conv),
		agent.WithConversationSize(20),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	// Same conversation ID for all turns in this session so history is shared.
	convID := "session-1"
	if id := os.Getenv("CONVERSATION_ID"); id != "" {
		convID = id
	}

	// Use first arg as single prompt, or run interactive if no args.
	if len(os.Args) > 1 {
		prompt := strings.Join(os.Args[1:], " ")
		runSingleTurn(context.Background(), a, prompt, convID)
		return
	}

	runInteractive(context.Background(), a, convID)
}

func runSingleTurn(ctx context.Context, a *agent.Agent, prompt, convID string) {
	fmt.Println("user:", prompt)
	response, err := a.Run(ctx, prompt, convID)
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	fmt.Println("assistant:", response.Content)
}

func runInteractive(ctx context.Context, a *agent.Agent, convID string) {
	fmt.Println("Multi-turn conversation. Use same conversation ID for context. Type 'exit' or 'quit' to end.")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\nuser: ")
		if !scanner.Scan() {
			break
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}
		if prompt == "exit" || prompt == "quit" || prompt == "bye" {
			break
		}
		response, err := a.Run(ctx, prompt, convID)
		if err != nil {
			log.Printf("run failed: %v", err)
			continue
		}
		fmt.Println("assistant:", response.Content)
	}
}
