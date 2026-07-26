// Worker process for the agent_with_reconnect example.
//
// Usage:
//
//	AGENT_RUNTIME=temporal go run ./worker
//
// Start this before running the agent. The worker registers the AgentWorkflow
// and all activities on the configured Temporal task queue.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/agent_with_reconnect/opts"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	workerOpts := opts.Common(cfg.Host, cfg.Port, cfg.Namespace, cfg.TaskQueue, llmClient, config.NewLoggerFromLogConfig(cfg))
	w, err := agent.NewAgentWorker(workerOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent worker", err))
	}

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	fmt.Printf("agent_with_reconnect worker starting on task queue %q.\n", cfg.TaskQueue)
	go func() {
		fmt.Println("Worker running. Press Ctrl+C to stop.")
		if err := w.Start(context.Background()); err != nil {
			log.Printf("worker stopped: %v", err)
		}
	}()

	<-sigChan
	fmt.Println("Shutdown signal received; stopping worker...")
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
		fmt.Println("Worker stopped.")
	case <-sigChan:
		fmt.Println("Second signal: forcing exit.")
		os.Exit(1)
	}
}
