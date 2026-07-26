package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation/redis"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	// Redis conversation (start with task infra:redis:up or docker compose redis service).
	conv, err := redis.NewConversation(
		redis.WithAddr(redisAddrFromEnv()),
		redis.WithMaxSize(100),
	)
	if err != nil {
		log.Fatalf("failed to create Redis conversation: %v", err)
	}
	defer func() { _ = conv.Close() }()

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		echo.New(),
		calculator.New(),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}
	opts := []agent.Option{
		agent.WithName("agent-with-conversation"),
		agent.WithDescription("Agent with Redis conversation and tools for multi-turn context"),
		agent.WithSystemPrompt("You are a helpful assistant. Remember the conversation context. Use tools when helpful: echo for repeating, calculator for math."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithConversation(conversation.Config{
			Conversation:    conv,
			Size:            20,
			SaveOnIteration: true,
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
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

func redisAddrFromEnv() string {
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		return addr
	}
	return "localhost:6379"
}

func runSingleTurn(ctx context.Context, a *agent.Agent, prompt, convID string) {
	fmt.Println("user:", prompt)
	opts := &agent.AgentRunOptions{
		ConversationOptions: &agent.ConversationOptions{
			ID: convID,
		},
	}
	agentRun, err := a.Run(ctx, prompt, opts)
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	result, err := agentRun.Get(ctx)
	if err != nil {
		log.Printf("run get result: %v", err)
		return
	}
	fmt.Println("assistant:", result.Content)
	shared.PrintRunFooters(result)
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
		opts := &agent.AgentRunOptions{
			ConversationOptions: &agent.ConversationOptions{
				ID: convID,
			},
		}
		agentRun, err := a.Run(ctx, prompt, opts)
		if err != nil {
			log.Printf("run failed: %v", err)
			continue
		}
		result, err := agentRun.Get(ctx)
		if err != nil {
			log.Printf("run get result: %v", err)
			continue
		}
		fmt.Println("assistant:", result.Content)
		shared.PrintRunFooters(result)
	}
}
