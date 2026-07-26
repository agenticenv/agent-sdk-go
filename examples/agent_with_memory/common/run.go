package common

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
)

const (
	// DefaultStorePrompt is run 1 in the two-turn demo.
	DefaultStorePrompt = "Remember for all future runs: I prefer concise answers. Persist this preference to long-term memory before you reply."
	// defaultStoreRetryPrompt is used when the first on-demand store run did not persist anything.
	defaultStoreRetryPrompt = "Persist this to long-term memory before replying: I prefer concise answers in all future conversations."
	// DefaultRecallPrompt is run 2 in the two-turn demo.
	DefaultRecallPrompt = "What answer style do I prefer?"
)

// ScopedContext attaches the demo user id for memory scope resolution.
func ScopedContext(ctx context.Context, userID string) context.Context {
	return memory.WithContextUserID(ctx, userID)
}

// RunAgent executes one prompt and prints the assistant reply plus optional footers.
func RunAgent(ctx context.Context, a *agent.Agent, userID, label, prompt string) *agent.AgentRunResult {
	fmt.Printf("\n--- %s ---\n", label)
	fmt.Println("user:", prompt)
	scopedCtx := ScopedContext(ctx, userID)
	agentRun, err := a.Run(scopedCtx, prompt, nil)
	if err != nil {
		log.Printf("%s failed: %v", label, err)
		return nil
	}
	result, err := agentRun.Get(scopedCtx)
	if err != nil {
		log.Printf("%s get result failed: %v", label, err)
		return nil
	}
	fmt.Println("assistant:", result.Content)
	shared.PrintRunFooters(result)
	return result
}

func memoryStores(result *agent.AgentRunResult) int64 {
	if result == nil || result.Telemetry == nil {
		return 0
	}
	return result.Telemetry.Storage.TotalMemoryStores
}

func runOnDemandStoreDemo(ctx context.Context, a *agent.Agent, userID string) {
	result := RunAgent(ctx, a, userID, "run 1 (save_memory)", DefaultStorePrompt)
	if memoryStores(result) > 0 {
		return
	}
	fmt.Println("warning: no memory was stored on run 1 (the model may have skipped the memory tool); retrying")
	RunAgent(ctx, a, userID, "run 1 retry (save_memory)", defaultStoreRetryPrompt)
}

// RunFromArgs runs the two-turn store/recall demo when no CLI args are given; otherwise a single custom prompt.
func RunFromArgs(ctx context.Context, a *agent.Agent, userID string, storeMode memory.StoreMode) {
	args := os.Args[1:]
	if len(args) == 0 {
		run1Label := "run 1 (save_memory)"
		if storeMode == memory.StoreModeAlways {
			run1Label = "run 1 (run-end store)"
		}
		if storeMode == memory.StoreModeOnDemand {
			runOnDemandStoreDemo(ctx, a, userID)
		} else {
			RunAgent(ctx, a, userID, run1Label, DefaultStorePrompt)
		}
		RunAgent(ctx, a, userID, "run 2 (recall)", DefaultRecallPrompt)
		return
	}
	RunAgent(ctx, a, userID, "run", strings.Join(args, " "))
}
