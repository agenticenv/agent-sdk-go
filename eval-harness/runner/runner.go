package main

import (
	"context"
	"fmt"

	"github.com/agenticenv/agent-sdk-go/eval-harness/runner/setup"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
)

// RunOutcome is the result of an eval harness execution.
type RunOutcome struct {
	Result         *agent.AgentRunResult
	MemoryScenario *MemoryScenarioOutcome
}

// MemoryScenarioOutcome holds store/recall runs for memory regression checks.
type MemoryScenarioOutcome struct {
	Store  *agent.AgentRunResult
	Recall *agent.AgentRunResult
}

// Run executes one agent run with mock LLM and mock tools, then closes the agent.
func Run(ctx context.Context, cfg setup.Config) (*RunOutcome, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if cfg.UsesMemoryScenario() {
		return runMemoryStoreRecall(ctx, cfg)
	}

	a, err := setup.BuildAgent(cfg)
	if err != nil {
		return nil, err
	}
	defer a.Close()

	agentRun, err := a.Run(ctx, cfg.UserPrompt, nil)
	if err != nil {
		return nil, fmt.Errorf("agent run: %w", err)
	}
	result, err := agentRun.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent run get result: %w", err)
	}
	return &RunOutcome{Result: result}, nil
}

func runMemoryStoreRecall(ctx context.Context, cfg setup.Config) (*RunOutcome, error) {
	a, err := setup.BuildAgent(cfg)
	if err != nil {
		return nil, err
	}
	defer a.Close()

	scoped := memory.WithContextUserID(ctx, cfg.Memory.UserID)

	storeRun, err := a.Run(scoped, cfg.Memory.StorePrompt, nil)
	if err != nil {
		return nil, fmt.Errorf("memory store run: %w", err)
	}
	storeResult, err := storeRun.Get(scoped)
	if err != nil {
		return nil, fmt.Errorf("memory store run get result: %w", err)
	}

	recallRun, err := a.Run(scoped, cfg.Memory.RecallPrompt, nil)
	if err != nil {
		return nil, fmt.Errorf("memory recall run: %w", err)
	}
	recallResult, err := recallRun.Get(scoped)
	if err != nil {
		return nil, fmt.Errorf("memory recall run get result: %w", err)
	}

	return &RunOutcome{
		Result: recallResult,
		MemoryScenario: &MemoryScenarioOutcome{
			Store:  storeResult,
			Recall: recallResult,
		},
	}, nil
}
