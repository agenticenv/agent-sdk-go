package agent

import (
	"context"

	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
)

// AgentWorker runs the Temporal worker for an agent. It owns run worker creation and
// registration of workflows and activities from agent_workflow.go.
type AgentWorker struct {
	agentConfig
	runtime runtime.Runtime
}

// NewAgentWorker creates an AgentWorker that polls and executes agent workflows.
// Same options as NewAgent. Use when the agent is created with DisableLocalWorker().
func NewAgentWorker(opts ...Option) (*AgentWorker, error) {
	cfg, err := buildAgentConfig(opts)
	if err != nil {
		return nil, err
	}
	cfg.remoteWorker = true
	rt, err := cfg.buildAgentRuntime(true)
	if err != nil {
		return nil, err
	}
	return &AgentWorker{agentConfig: *cfg, runtime: rt}, nil
}

// Start starts the worker (blocks until Stop is called).
func (aw *AgentWorker) Start(ctx context.Context) error {
	aw.logger.Info(ctx, "agent worker starting", slog.String("taskQueue", aw.taskQueue))
	return aw.runtime.Start(ctx)
}

// Stop stops the worker.
func (aw *AgentWorker) Stop() {
	aw.logger.Info(context.Background(), "agent worker stopping", slog.String("taskQueue", aw.taskQueue))
	aw.runtime.Stop()
}
