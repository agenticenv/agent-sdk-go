package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"

	config "github.com/vinodvanja/temporal-agents-go/examples"
	"github.com/vinodvanja/temporal-agents-go/examples/agent_with_worker/opts"
	"github.com/vinodvanja/temporal-agents-go/pkg/agent"
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
		log.Fatalf("failed to create agent worker: %v", err)
	}
	defer w.Close()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	fmt.Printf("Agent worker starting on task queue %q. Run this before the agent.\n", cfg.TaskQueue)
	// Start() blocks until Close() is called. Run it in a goroutine so we can handle shutdown.
	go func() {
		fmt.Println("Agent worker running. Press Ctrl+C to stop.")
		if err := w.Start(); err != nil {
			log.Printf("worker stopped: %v", err)
		}
	}()

	<-sigChan
	fmt.Println("Shutting down agent worker...")
}
