package agent

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// AgentWorker runs the Temporal worker for an agent. It owns run worker creation and
// registration of workflows and activities from agent_workflow.go.
type AgentWorker struct {
	config       *agentConfig
	agentChannel *agentChannel // set when local (same process); nil when remote
	worker       worker.Worker
}

// createAgentWorker creates and registers a Temporal worker for the agent's run workflow and activities.
func createAgentWorker(aw *AgentWorker) worker.Worker {
	aw.config.logger.Debug("creating agent worker", zap.String("taskQueue", aw.config.taskQueue))
	w := worker.New(aw.config.temporalClient, aw.config.taskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(aw.AgentWorkflow, workflow.RegisterOptions{Name: "AgentWorkflow"})
	w.RegisterActivityWithOptions(aw.AgentLLMActivity, activity.RegisterOptions{Name: "AgentLLMActivity"})
	w.RegisterActivityWithOptions(aw.AgentLLMStreamActivity, activity.RegisterOptions{Name: "AgentLLMStreamActivity"})
	w.RegisterActivityWithOptions(aw.AgentToolApprovalActivity, activity.RegisterOptions{Name: "AgentToolApprovalActivity"})
	w.RegisterActivityWithOptions(aw.AgentToolExecuteActivity, activity.RegisterOptions{Name: "AgentToolExecuteActivity"})
	w.RegisterActivityWithOptions(aw.SendAgentEventUpdateActivity, activity.RegisterOptions{Name: "SendAgentEventUpdateActivity"})
	w.RegisterActivityWithOptions(aw.AddConversationMessagesActivity, activity.RegisterOptions{Name: "AddConversationMessagesActivity"})
	return w
}

// newAgentWorkerFromConfig creates an AgentWorker from agentConfig.
// agentChannel is passed when workers run locally (same process); nil for remote workers.
func newAgentWorkerFromConfig(cfg *agentConfig, ch *agentChannel) *AgentWorker {
	aw := &AgentWorker{config: cfg, agentChannel: ch}
	aw.worker = createAgentWorker(aw)
	return aw
}

// NewAgentWorkerDefault returns an AgentWorker for workflow type identification (ExecuteWorkflow). Used by Agent when no local worker.
func NewAgentWorkerDefault() *AgentWorker {
	return &AgentWorker{}
}

// NewAgentWorker creates an AgentWorker that polls and executes agent workflows.
// Same options as NewAgent. Use when the agent is created with DisableWorker().
func NewAgentWorker(opts ...Option) (*AgentWorker, error) {
	cfg, err := buildAgentConfigForWorker(opts)
	if err != nil {
		return nil, err
	}
	cfg.remoteWorker = true
	return newAgentWorkerFromConfig(cfg, nil), nil
}

// Start starts the worker (blocks until Stop is called).
func (aw *AgentWorker) Start() error {
	if aw.config != nil && aw.config.logger != nil {
		aw.config.logger.Info("agent worker starting", zap.String("taskQueue", aw.config.taskQueue))
	}
	return aw.worker.Start()
}

// stop stops the worker. Unexported; Agent calls it when closing embedded worker.
func (aw *AgentWorker) stop() {
	if aw.config != nil && aw.config.logger != nil {
		aw.config.logger.Debug("agent worker stopping", zap.String("taskQueue", aw.config.taskQueue))
	}
	aw.worker.Stop()
}

// Close stops the worker and closes the Temporal client. Call when AgentWorker is the top-level object (standalone process).
// Does not close the client when it was provided via WithTemporalClient (caller owns the lifecycle).
func (aw *AgentWorker) Close() {
	if aw.config != nil && aw.config.logger != nil {
		aw.config.logger.Info("agent worker closing", zap.String("taskQueue", aw.config.taskQueue))
	}
	aw.stop()
	if aw.config != nil && aw.config.temporalClient != nil && aw.config.ownsTemporalClient {
		aw.config.temporalClient.Close()
	}
}
