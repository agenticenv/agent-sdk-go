package agent

import (
	"context"

	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
)

// AgentWorker runs the execution runtime's worker for an agent (polls the task queue and executes runs).
type AgentWorker struct {
	agentConfig
	runtime runtime.Runtime
}

// NewAgentWorker creates an AgentWorker that polls and executes runs for the configured backend.
// Same options as [NewAgent]. Use when the agent is created with [DisableLocalWorker].
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
	aw.logger.Info(ctx, "agent worker starting", slog.String("scope", "agent"), slog.String("taskQueue", aw.taskQueue))
	return aw.runtime.Start(ctx)
}

// Stop stops the worker.
func (aw *AgentWorker) Stop() {
	aw.logger.Info(context.Background(), "agent worker stopping", slog.String("scope", "agent"), slog.String("taskQueue", aw.taskQueue))
	aw.runtime.Stop()
}
