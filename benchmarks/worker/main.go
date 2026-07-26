package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/agenticenv/agent-sdk-go/benchmarks/setup"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	configPath := flag.String("config", "", "path to benchmark config.yaml")
	workerID := flag.Int("worker-id", 1, "worker instance id for logging")
	flag.Parse()

	cfg, err := setup.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if !cfg.UseTemporal() {
		log.Fatal("benchmark worker requires runtime: temporal in config")
	}

	repoRoot, err := setup.FindRepoRoot(".")
	if err != nil {
		log.Fatalf("find repo root: %v", err)
	}

	lgr, closeLogger, err := setup.SetupWorkerLogger(cfg, repoRoot, *workerID)
	if err != nil {
		log.Fatalf("setup logger: %v", err)
	}
	defer closeLogger()

	llm := setup.NewMockLLMClient(cfg.LLM, setup.NewLLMStats(), setup.TreeRNG())
	tree, err := setup.BuildAgentTree(cfg, llm, lgr)
	if err != nil {
		log.Fatalf("build agent tree: %v", err)
	}
	defer setup.CloseAgents(tree.Created)

	opts, err := setup.AppendMemoryOptions(cfg, setup.RootOptions(cfg, llm, lgr, setup.RootAgentName, tree.RootPrompt, tree.SubAgents, cfg.Temporal.TaskQueue))
	if err != nil {
		log.Fatalf("memory options: %v", err)
	}
	w, err := agent.NewAgentWorker(opts...)
	if err != nil {
		log.Fatalf("create agent worker: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("benchmark worker %d starting on task queue %q\n", *workerID, cfg.Temporal.TaskQueue)

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	select {
	case err := <-done:
		if err != nil && ctx.Err() == nil {
			log.Fatalf("worker stopped: %v", err)
		}
	case <-ctx.Done():
		w.Stop()
		<-done
	}

	fmt.Printf("benchmark worker %d stopped\n", *workerID)
}
