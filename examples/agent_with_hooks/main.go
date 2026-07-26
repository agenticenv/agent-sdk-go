// Example agent demonstrating all middleware hook points.
//
// Run from examples/:
//
//	go run ./agent_with_hooks
//	go run ./agent_with_hooks "My email is alice@example.com. What is the return policy?"
//
// Hook activity is printed to stderr with a [hooks] prefix. When AGENT_RUNTIME=temporal,
// register the same [HookOptions] on both the agent starter and the worker process.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
)

const demoTenantID = "tenant-demo"
const demoUserID = "user-demo"

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	memStore := newDemoMemory()
	memCfg := memory.DefaultConfig(memStore)
	memCfg.Store.Mode = memory.StoreModeAlways
	memCfg.Store.Extract = demoMemoryExtract
	memCfg.Recall.Enabled = true
	memCfg.Recall.Limit = 5

	opts := []agent.Option{
		agent.WithName("agent-with-hooks"),
		agent.WithDescription("Demonstrates all agent middleware hooks"),
		agent.WithSystemPrompt(
			"You are a helpful assistant. Use retrieved knowledge when present. " +
				"Use tools when asked for calculations or echo. " +
				"Answer concisely in bullet points when the user asks about preferences.",
		),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
		agent.WithTools(echo.New(), calculator.New()),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithRetrievers(demoRetriever{}),
		agent.WithRetrieverMode(agent.RetrieverModePrefetch),
		agent.WithMemory(memCfg),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)
	opts = append(opts, config.ToolApprovalOptions()...)
	opts = append(opts, HookOptions()...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	ctx := memory.WithContextUserID(
		memory.WithContextTenantID(context.Background(), demoTenantID),
		demoUserID,
	)

	args := os.Args[1:]
	if len(args) == 0 {
		runDemo(ctx, a)
		return
	}
	runOnce(ctx, a, "custom", strings.Join(args, " "))
}

func runDemo(ctx context.Context, a *agent.Agent) {
	fmt.Println("=== agent_with_hooks demo (two runs) ===")
	fmt.Println("Hook log lines go to stderr. Look for [hooks] BeforeLLM, AfterRetrieve, BeforeMemoryStore, etc.")
	fmt.Println()

	runOnce(ctx, a, "run 1 (store + prefetch + tools)",
		"My email is alice@example.com. "+
			"What is the return policy according to the knowledge base? "+
			"Echo the phrase hooks-demo-ok and compute 12 * 8.",
	)

	runOnce(ctx, a, "run 2 (memory recall)",
		"What answer style do I prefer?",
	)
}

func runOnce(ctx context.Context, a *agent.Agent, label, prompt string) {
	fmt.Printf("\n--- %s ---\n", label)
	fmt.Println("user:", prompt)
	fmt.Fprintln(os.Stderr, "--- hook activity ---")

	agentRun, err := a.Run(ctx, prompt, nil)
	if err != nil {
		log.Printf("%s failed: %v", label, err)
		return
	}
	result, err := agentRun.Get(ctx)
	if err != nil {
		return
	}
	fmt.Println("assistant:", result.Content)
	shared.PrintRunFooters(result)
}
