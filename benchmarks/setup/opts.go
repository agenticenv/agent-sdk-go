package setup

import (
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentrestate "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate"
	agenttemporal "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

// RootOptions returns agent options shared by the benchmark root agent and root worker.
func RootOptions(
	cfg *Config,
	llm interfaces.LLMClient,
	lgr logger.Logger,
	name, systemPrompt string,
	subAgents []*agent.Agent,
	taskQueue string,
) []agent.Option {
	if lgr == nil {
		lgr = logger.NoopLogger()
	}

	opts := []agent.Option{
		agent.WithName(name),
		agent.WithDescription("Benchmark agent for SDK load testing."),
		agent.WithSystemPrompt(systemPrompt),
		agent.WithLLMClient(llm),
		agent.WithToolRegistry(RegisterBenchmarkTools(cfg.Agent.Tools.Count, cfg.Tool, TreeRNG())),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithAgentToolExecutionMode(mapToolExecutionMode(cfg.Agent.Tools.Execution)),
		agent.WithLogger(lgr),
	}
	if len(subAgents) > 0 {
		opts = append(opts, agent.WithSubAgents(subAgents...))
	}
	if cfg.Agent.Subagents.Levels > 0 {
		opts = append(opts, agent.WithMaxSubAgentDepth(cfg.Agent.Subagents.Levels))
	}
	switch {
	case cfg.UseTemporal():
		opts = append(opts, agenttemporal.WithTemporalConfig(&agenttemporal.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: taskQueue,
		}))
	case cfg.UseRestate():
		opts = append(opts, agentrestate.WithRestateConfig(&agentrestate.RestateConfig{
			Ingress: agentrestate.IngressConfig{
				URL:     cfg.Restate.IngressURL,
				AuthKey: cfg.Restate.AuthKey,
			},
			Endpoint: agentrestate.EndpointConfig{
				ListenAddress: cfg.Restate.EndpointListenAddress,
				AdminURL:      cfg.Restate.AdminURL,
				DeploymentURL: cfg.Restate.DeploymentURL,
			},
		}))
	}
	return opts
}

// AppendMemoryOptions adds WithMemory when memory is enabled in cfg.
func AppendMemoryOptions(cfg *Config, opts []agent.Option) ([]agent.Option, error) {
	memOpt, err := MemoryAgentOption(cfg)
	if err != nil {
		return nil, err
	}
	if memOpt != nil {
		opts = append(opts, memOpt)
	}
	return opts, nil
}

func mapToolExecutionMode(raw string) agent.AgentToolExecutionMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sequential":
		return agent.AgentToolExecutionModeSequential
	default:
		return agent.AgentToolExecutionModeParallel
	}
}

func TaskQueueFor(cfg *Config, suffix string) string {
	base := strings.TrimSpace(cfg.Temporal.TaskQueue)
	if suffix == "" {
		return base
	}
	return base + "-" + suffix
}

func CloseAgents(agents []*agent.Agent) {
	for _, a := range agents {
		if a != nil {
			a.Close()
		}
	}
}
