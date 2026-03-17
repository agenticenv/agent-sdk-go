package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	config "github.com/vvsynapse/temporal-agents-go/examples"
	"github.com/vvsynapse/temporal-agents-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Printf("failed to create LLM client: %v", err)
		return
	}

	// TaskQueue must be unique per agent. Use WithInstanceId when running multiple agents in same process.
	temporalCfg := &agent.TemporalConfig{
		Host:      cfg.Host,
		Port:      cfg.Port,
		Namespace: cfg.Namespace,
		TaskQueue: cfg.TaskQueue,
	}

	agent1, err := agent.NewAgent(
		agent.WithName("agent-1"),
		agent.WithSystemPrompt("You are a helpful math assistant. Keep answers brief."),
		agent.WithTemporalConfig(temporalCfg),
		agent.WithInstanceId("agent-1"),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	)
	if err != nil {
		log.Fatalf("failed to create agent 1: %v", err)
	}
	defer agent1.Close()

	agent2, err := agent.NewAgent(
		agent.WithName("agent-2"),
		agent.WithSystemPrompt("You are a creative writing assistant. Be expressive."),
		agent.WithTemporalConfig(temporalCfg),
		agent.WithInstanceId("agent-2"),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	)
	if err != nil {
		log.Fatalf("failed to create agent 2: %v", err)
	}
	defer agent2.Close()

	mode, prompt := parseArgs()
	if prompt == "" {
		prompt = "What is 7 times 8?"
	}

	runAgent := func(name string, a *agent.Agent, p string) {
		fmt.Printf("\n--- %s ---\n", name)
		response, err := a.Run(context.Background(), p, "")
		if err != nil {
			fmt.Printf("%s error: %v\n", name, err)
			return
		}
		fmt.Printf("%s: %s\n", name, response.Content)
	}

	if mode == "concurrent" {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			runAgent("Agent 1 (math)", agent1, prompt)
		}()
		go func() {
			defer wg.Done()
			runAgent("Agent 2 (creative)", agent2, prompt)
		}()
		wg.Wait()
	} else {
		// sequential (default)
		runAgent("Agent 1 (math)", agent1, prompt)
		runAgent("Agent 2 (creative)", agent2, prompt)
	}
	fmt.Println("\nDone.")
}

// parseArgs returns (mode, prompt). First arg "sequential" or "concurrent" sets mode; else default sequential.
func parseArgs() (mode, prompt string) {
	mode = "sequential"
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "sequential" || args[0] == "concurrent") {
		mode = args[0]
		args = args[1:]
	}
	prompt = strings.Join(args, " ")
	return mode, prompt
}
