package setup

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentrestate "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate"
	agenttemporal "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal"
)

const evalRNGSeed int64 = 42

// RuntimeOption returns Temporal or Restate NewAgent options for cfg, or nil for local.
func RuntimeOption(cfg Config) []agent.Option {
	switch {
	case cfg.UseTemporal():
		return []agent.Option{
			agenttemporal.WithTemporalConfig(&agenttemporal.TemporalConfig{
				Host:      cfg.Temporal.Host,
				Port:      cfg.Temporal.Port,
				Namespace: cfg.Temporal.Namespace,
				TaskQueue: cfg.Temporal.TaskQueue,
			}),
		}
	case cfg.UseRestate():
		return []agent.Option{
			agentrestate.WithRestateConfig(&agentrestate.RestateConfig{
				Ingress: agentrestate.IngressConfig{
					URL:     cfg.Restate.IngressURL,
					AuthKey: cfg.Restate.AuthKey,
				},
				Endpoint: agentrestate.EndpointConfig{
					ListenAddress: cfg.Restate.EndpointListenAddress,
					AdminURL:      cfg.Restate.AdminURL,
					DeploymentURL: cfg.Restate.DeploymentURL,
				},
			}),
		}
	default:
		return nil
	}
}

// BuildAgent constructs an agent from cfg using mock LLM and tools when not overridden.
func BuildAgent(cfg Config) (*agent.Agent, error) {
	rng := rand.New(rand.NewSource(evalRNGSeed))

	llmClient := cfg.LLMClient
	if llmClient == nil {
		llmClient = NewMockLLMClient(cfg.LLM, rng)
	}

	toolRegistry := cfg.ToolRegistry
	if toolRegistry == nil {
		toolRegistry = RegisterMockTools(cfg.ToolCount, cfg.Tool, rng)
	}

	opts := []agent.Option{
		agent.WithName(cfg.AgentName),
		agent.WithDescription("Eval harness agent for single-run testing."),
		agent.WithSystemPrompt(cfg.SystemPrompt),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(toolRegistry),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(cfg.Logger),
	}
	opts = append(opts, RuntimeOption(cfg)...)

	memOpt, err := MemoryAgentOption(cfg)
	if err != nil {
		return nil, fmt.Errorf("memory option: %w", err)
	}
	if memOpt != nil {
		opts = append(opts, memOpt)
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		return nil, fmt.Errorf("new agent: %w", err)
	}
	if cfg.UseDurableRuntime() {
		time.Sleep(300 * time.Millisecond)
	}
	return a, nil
}
