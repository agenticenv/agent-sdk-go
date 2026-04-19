package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/durable_agent/opts"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	// Common opts (name, description, system prompt, Temporal, LLM, logger)
	workerOpts := opts.Common(cfg.Host, cfg.Port, cfg.Namespace, cfg.TaskQueue, llmClient, config.NewLoggerFromLogConfig(cfg))

	w, err := agent.NewAgentWorker(workerOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent worker", err))
	}

	// Buffer 2 so first signal is consumed and a second Ctrl+C can force-exit if Stop() blocks.
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	fmt.Printf("Agent worker starting on task queue %q. Run this before the agent.\n", cfg.TaskQueue)
	go func() {
		fmt.Println("Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).")
		if err := w.Start(context.Background()); err != nil {
			log.Printf("worker stopped: %v", err)
		}
	}()

	<-sigChan
	fmt.Println("Shutdown signal received; stopping worker (may wait for in-flight activities)...")

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		fmt.Println("Agent worker stopped.")
	case <-sigChan:
		fmt.Println("Second signal: forcing exit.")
		os.Exit(1)
	}
}
