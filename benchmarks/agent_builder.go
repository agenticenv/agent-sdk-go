package main

import (
	"fmt"
	"time"

	"github.com/agenticenv/agent-sdk-go/benchmarks/setup"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

type AgentBundle struct {
	Root *agent.Agent
	All  []*agent.Agent
}

func buildAgentBundle(cfg *setup.Config, llm *setup.MockLLMClient, lgr logger.Logger, tree *setup.AgentTree) (*AgentBundle, error) {
	opts, err := setup.AppendMemoryOptions(cfg, setup.RootOptions(cfg, llm, lgr, setup.RootAgentName, tree.RootPrompt, tree.SubAgents, cfg.Temporal.TaskQueue))
	if err != nil {
		return nil, err
	}

	root, err := agent.NewAgent(opts...)
	if err != nil {
		return nil, err
	}
	if cfg.UseDurableRuntime() {
		time.Sleep(300 * time.Millisecond)
	}

	all := append([]*agent.Agent{root}, tree.Created...)
	return &AgentBundle{Root: root, All: all}, nil
}

func buildAgentPool(cfg *setup.Config, llm *setup.MockLLMClient, lgr logger.Logger, size int) ([]*AgentBundle, error) {
	if size <= 0 {
		size = 1
	}
	bundles := make([]*AgentBundle, 0, size)
	for i := 0; i < size; i++ {
		tree, err := setup.BuildAgentTree(cfg, llm, lgr)
		if err != nil {
			for _, b := range bundles {
				setup.CloseAgents(b.All)
			}
			return nil, fmt.Errorf("agent tree index %d: %w", i, err)
		}
		bundle, err := buildAgentBundle(cfg, llm, lgr, tree)
		if err != nil {
			setup.CloseAgents(tree.Created)
			for _, b := range bundles {
				setup.CloseAgents(b.All)
			}
			return nil, fmt.Errorf("agent pool index %d: %w", i, err)
		}
		bundles = append(bundles, bundle)
	}
	return bundles, nil
}
