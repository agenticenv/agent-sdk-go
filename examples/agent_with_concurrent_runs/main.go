// agent_with_concurrent_runs shows multiple Run calls dispatched concurrently on a single
// Agent instance. The same agent handles all requests in parallel — no separate Agent per
// request needed. Works with both the local (default) and Temporal runtimes; toggle with
// AGENT_RUNTIME=temporal.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

// defaultPrompts are used when no CLI args are provided.
var defaultPrompts = []string{
	"What is the capital of France?",
	"What is 17 multiplied by 23?",
	"Name three planets in our solar system.",
	"What is the boiling point of water in Celsius?",
	"What color is the sky on a clear day?",
}

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("concurrent-agent"),
		agent.WithDescription("Concurrent runs demo — single agent instance, many parallel Run calls"),
		agent.WithSystemPrompt("You are a concise assistant. Answer in one sentence."),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompts := os.Args[1:]
	if len(prompts) == 0 {
		prompts = defaultPrompts
	}

	fmt.Printf("Dispatching %d concurrent runs on a single agent (runtime=%s)...\n\n",
		len(prompts), cfg.AgentRuntime)

	type result struct {
		idx    int
		prompt string
		runID  string
		res    *agent.AgentRunResult
		err    error
	}

	resultCh := make(chan result, len(prompts))
	var completed atomic.Int32
	var wg sync.WaitGroup

	for i, prompt := range prompts {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			// runID is available synchronously before the run completes.
			agentRun, err := a.Run(context.Background(), p, nil)
			if err != nil {
				resultCh <- result{idx: idx, prompt: p, err: err}
				return
			}
			runID := agentRun.ID()
			res, getErr := agentRun.Get(context.Background())
			resultCh <- result{idx: idx, prompt: p, runID: runID, res: res, err: getErr}
		}(i, prompt)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Print results as they arrive (arrival order varies run to run).
	ordered := make([]result, len(prompts))
	for r := range resultCh {
		n := int(completed.Add(1))
		ordered[r.idx] = r
		if r.err != nil {
			fmt.Printf("[%d/%d] ERROR  Q: %s\n       err: %v\n\n",
				n, len(prompts), r.prompt, r.err)
		} else {
			content := ""
			if r.res != nil {
				content = r.res.Content
			}
			fmt.Printf("[%d/%d] Q: %s\n       A: %s\n       %s\n\n",
				n, len(prompts), r.prompt, content, shared.RunIDLine(r.runID))
		}
	}

	// Optional: print per-run token usage / telemetry when SHOW_LLM_USAGE / SHOW_TELEMETRY are set.
	if shared.ShowLLMUsage() || shared.ShowTelemetry() {
		fmt.Println("--- Per-run footers (in prompt order) ---")
		for _, r := range ordered {
			if r.res != nil {
				fmt.Printf("  [run %d] %s\n", r.idx+1, r.prompt)
				shared.PrintRunFooters(r.res)
			}
		}
	}
}
