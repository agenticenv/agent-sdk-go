package agent

import "go.temporal.io/sdk/worker"

// AgentWorker runs the Temporal worker for an agent (standalone process).
// Use this when the agent is created with DisableWorker().
type AgentWorker struct {
	agent  *Agent
	worker worker.Worker
}

// NewAgentWorker creates an AgentWorker that polls and executes agent workflows.
// Same options as NewAgent. Note: remote worker mode is not supported (use embedded worker).
func NewAgentWorker(opts ...Option) (*AgentWorker, error) {
	opts = append(opts, func(a *Agent) { a.agentWorker = true })
	a, err := buildAgent(opts)
	if err != nil {
		return nil, err
	}
	a.remoteWorker = true

	return &AgentWorker{
		agent:  a,
		worker: createRunWorker(a),
	}, nil
}

// Start starts the worker (blocks until Stop is called).
func (w *AgentWorker) Start() error {
	return w.worker.Start()
}

// Close stops the worker and closes the client.
func (w *AgentWorker) Close() {
	w.worker.Stop()
	w.agent.Close()
}
