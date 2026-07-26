package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		echo.New(),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}
	opts := []agent.Option{
		agent.WithName("agent-with-a2a-server"),
		agent.WithDescription("Example agent exposed as an A2A HTTP server (agent card + JSON-RPC)."),
		agent.WithSystemPrompt("You are a helpful assistant. You have an echo tool when the user asks to repeat text."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		config.A2AInboundServerOption(cfg),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
		agent.WithLogLevel(cfg.LogLevel),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	base := config.A2AServerDisplayURL(cfg)
	cardURL := base + a2asrv.WellKnownAgentCardPath

	fmt.Fprintln(os.Stderr, "A2A server (built-in)")
	fmt.Fprintf(os.Stderr, "  Base URL   %s\n", base)
	fmt.Fprintf(os.Stderr, "  Agent card %s\n", cardURL)
	fmt.Fprintln(os.Stderr, "Press Ctrl+C to stop.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		err := a.RunA2A(ctx)
		if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			log.Printf("RunA2A: %v", err)
		}
	}()

	<-ctx.Done()
	<-done
}
