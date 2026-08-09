package setup

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

type AgentTree struct {
	RootPrompt string
	SubAgents  []*agent.Agent
	Created    []*agent.Agent
}

func BuildAgentTree(cfg *Config, llm interfaces.LLMClient, lgr logger.Logger) (*AgentTree, error) {
	treeRng := TreeRNG()
	rootPrompt := RootSystemPrompt(treeRng)

	subAgents, created, err := buildSubAgentTree(cfg, llm, lgr, treeRng, "", 1, cfg.Agent.Subagents.Levels)
	if err != nil {
		CloseAgents(created)
		return nil, err
	}

	return &AgentTree{
		RootPrompt: rootPrompt,
		SubAgents:  subAgents,
		Created:    created,
	}, nil
}

func buildSubAgentTree(
	cfg *Config,
	llm interfaces.LLMClient,
	lgr logger.Logger,
	treeRng *rand.Rand,
	parentPath string,
	depth, maxLevels int,
) ([]*agent.Agent, []*agent.Agent, error) {
	if depth > maxLevels || cfg.Agent.Subagents.Count == 0 {
		return nil, nil, nil
	}

	subAgents := make([]*agent.Agent, 0, cfg.Agent.Subagents.Count)
	created := make([]*agent.Agent, 0)

	for i := 1; i <= cfg.Agent.Subagents.Count; i++ {
		nameSuffix := fmt.Sprintf("%d", i)
		if parentPath != "" {
			nameSuffix = parentPath + "." + nameSuffix
		}
		displayName := "subagent-" + nameSuffix
		queueSuffix := strings.ReplaceAll(displayName, ".", "-")

		children, childCreated, err := buildSubAgentTree(cfg, llm, lgr, treeRng, nameSuffix, depth+1, maxLevels)
		if err != nil {
			CloseAgents(created)
			return nil, nil, err
		}
		created = append(created, childCreated...)

		sub, err := newSubAgent(cfg, llm, lgr, treeRng, displayName, systemPrompt(treeRng), children, TaskQueueFor(cfg, queueSuffix))
		if err != nil {
			CloseAgents(created)
			return nil, nil, err
		}
		subAgents = append(subAgents, sub)
		created = append(created, sub)
	}

	return subAgents, created, nil
}

func newSubAgent(
	cfg *Config,
	llm interfaces.LLMClient,
	lgr logger.Logger,
	treeRng *rand.Rand,
	name, prompt string,
	subAgents []*agent.Agent,
	taskQueue string,
) (*agent.Agent, error) {
	opts := RootOptions(cfg, llm, lgr, name, prompt, subAgents, taskQueue)
	a, err := agent.NewAgent(opts...)
	if err != nil {
		return nil, err
	}
	if cfg.UseDurableRuntime() {
		time.Sleep(300 * time.Millisecond)
	}
	_ = treeRng
	return a, nil
}
