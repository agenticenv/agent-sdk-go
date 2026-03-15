package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

const (
	// defaultTimeout is used when DisableWorker but no deadline set, to avoid blocking forever when no workers run.
	defaultTimeout = 5 * time.Minute
	// workersCheckTimeout is how long hasWorkers polls for pollers before giving up.
	workersCheckTimeout = 15 * time.Second
)

// TemporalConfig holds Temporal connection settings (Host, Port, Namespace) and the task queue name.
//
// TaskQueue is required and must be unique per agent. Use different TaskQueues when running
// multiple agents in the same process (e.g. "my-agent-math", "my-agent-creative").
// For multiple instances of the same agent (e.g. scaled pods), use WithInstanceId() so each
// instance gets a unique queue derived as {TaskQueue}-{InstanceId}.
//
// When using DisableWorker, the agent and NewAgentWorker must use the same TaskQueue (and
// same InstanceId if set) so they pair correctly.
type TemporalConfig struct {
	Host      string
	Port      int
	Namespace string
	TaskQueue string // Required. Full task queue name. Unique per agent.
}

// Agent runs LLM-backed workflows via Temporal. It holds agentConfig and client-side state
// (agentChannel, approvalHandler, event worker). Uses AgentWorker for run workflow execution.
type Agent struct {
	agentConfig
	localAgentWorker      *AgentWorker // run worker; set when workers are embedded
	eventWorker           worker.Worker
	agentChannel          *agentChannel
	eventWorkerMu         sync.Mutex
	runMu                 sync.Mutex
	activeRunWorkflowID   string
	activeEventWorkflowID string
}

// ErrAgentAlreadyRunning is returned when Run or RunStream is called while a run is already in progress.
var ErrAgentAlreadyRunning = errors.New("agent already has an active run")

// AgentResponse is the return value of AgentRun.Get / AgentWorkflow.
type AgentResponse struct {
	Content   string         `json:"content"`
	AgentName string         `json:"agent_name"`
	Model     string         `json:"model"`
	Metadata  map[string]any `json:"metadata"`
}

// buildAgent builds an Agent from options. Validates approval handler when tools require approval.
func buildAgent(opts []Option) (*Agent, error) {
	cfg, err := buildAgentConfig(opts)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		agentConfig:  *cfg,
		agentChannel: newAgentChannel(cfg.logger),
	}
	for _, t := range a.toolsList() {
		if a.requiresApproval(t) && a.approvalHandler == nil {
			return nil, fmt.Errorf("tool %q requires approval but WithApprovalHandler was not set", t.Name())
		}
	}
	if a.disableWorker && (a.approvalHandler != nil || a.streamEnabled) && !a.enableRemoteWorkers {
		return nil, fmt.Errorf("DisableWorker with approval or streaming requires WithEnableRemoteWorkers(true)")
	}

	if !a.disableWorker {
		a.localAgentWorker = newAgentWorkerFromConfig(cfg, a.agentChannel)
	}

	return a, nil
}

// hasWorkers returns true if there are pollers on the given task queue.
// If taskQueue is empty, uses a.taskQueue.
// Polls DescribeTaskQueue for up to workersCheckTimeout (default 15s) before returning false.
func (a *Agent) hasWorkers(ctx context.Context, taskQueue string) bool {
	q := taskQueue
	if q == "" {
		q = a.taskQueue
	}
	timeout := workersCheckTimeout
	deadline, ok := ctx.Deadline()
	if ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	deadlineTime := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		res, err := a.temporalClient.DescribeTaskQueue(ctx, q, enumspb.TASK_QUEUE_TYPE_WORKFLOW)
		if err == nil && len(res.GetPollers()) != 0 {
			a.logger.Debug("workers found on task queue", zap.String("taskQueue", q), zap.Int("pollers", len(res.GetPollers())))
			return true
		}
		if time.Now().After(deadlineTime) {
			a.logger.Debug("workers check timed out", zap.String("taskQueue", q))
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			// retry
		}
	}
}

// createEventWorker starts the event worker if not already running. Called when RunStream or
// approval handling is needed. Per-agent mutex allows parallel creation across different agents
// while preventing double-creation when Run and RunStream are invoked concurrently on the same agent.
func (a *Agent) createEventWorker() {
	a.eventWorkerMu.Lock()
	defer a.eventWorkerMu.Unlock()
	if a.eventWorker != nil {
		a.logger.Debug("event worker already running", zap.String("taskQueue", a.taskQueue))
		return
	}
	eventQueue := getEventTaskQueue(a.taskQueue)
	a.logger.Info("starting event worker", zap.String("taskQueue", eventQueue))
	w := worker.New(a.temporalClient, eventQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(a.AgentEventWorkflow, workflow.RegisterOptions{Name: "AgentEventWorkflow"})
	w.RegisterActivityWithOptions(a.EventPublishActivity, activity.RegisterOptions{Name: "EventPublishActivity"})
	w.RegisterActivityWithOptions(a.ToolRunApprovalPublishActivity, activity.RegisterOptions{Name: "ToolRunApprovalPublishActivity"})
	a.eventWorker = w
	go func() { _ = a.eventWorker.Start() }()
}

// NewAgent creates an Agent with the given options.
// Event worker is started lazily when RunStream is called or when approvals are needed.
func NewAgent(opts ...Option) (*Agent, error) {
	a, err := buildAgent(opts)
	if err != nil {
		return nil, err
	}
	a.logger.Info("agent created", zap.String("name", a.Name), zap.String("taskQueue", a.taskQueue), zap.Bool("embedWorker", a.localAgentWorker != nil))
	if a.localAgentWorker != nil {
		go func() { _ = a.localAgentWorker.Start() }()
	}
	return a, nil
}

// ensureEventWorkflowStarted starts the event workflow once per agent. Sets activeEventWorkflowID on first call.
func (a *Agent) ensureEventWorkflowStarted(ctx context.Context) (string, error) {
	a.runMu.Lock()
	id := a.activeEventWorkflowID
	a.runMu.Unlock()
	if id != "" {
		a.logger.Debug("reusing existing event workflow", zap.String("eventWorkflowID", id))
		return id, nil
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.activeEventWorkflowID != "" {
		return a.activeEventWorkflowID, nil
	}
	eid := eventWorkflowIDPrefix + a.Name + "-" + a.ID

	a.logger.Debug("executing event workflow", zap.String("eid", eid))

	_, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       eid,
		TaskQueue:                getEventTaskQueue(a.taskQueue),
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, a.AgentEventWorkflow)
	if err != nil {
		a.logger.Error("failed to start event workflow", zap.String("eid", eid), zap.Error(err))
		return "", err
	}
	a.activeEventWorkflowID = eid
	a.logger.Info("event workflow started", zap.String("eventWorkflowID", eid))
	return eid, nil
}

func getEventTaskQueue(taskQueue string) string {
	return taskQueue + "-events"
}

// beginRun marks the start of a run. Returns (cleanup, nil) on success; call cleanup (or defer) when the run ends.
// Returns (nil, ErrAgentAlreadyRunning) if a run is already active.
func (a *Agent) beginRun(workflowID string) (func(), error) {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.activeRunWorkflowID != "" {
		a.logger.Debug("beginRun rejected: already running", zap.String("active", a.activeRunWorkflowID), zap.String("requested", workflowID))
		return nil, ErrAgentAlreadyRunning
	}
	a.activeRunWorkflowID = workflowID
	a.logger.Debug("beginRun", zap.String("workflowID", workflowID))
	return func() { a.endRun() }, nil
}

// endRun clears the active run state when a run completes. Does not clear activeEventWorkflowID (per-agent).
func (a *Agent) endRun() {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.activeRunWorkflowID != "" {
		a.logger.Debug("endRun", zap.String("workflowID", a.activeRunWorkflowID))
	}
	a.activeRunWorkflowID = ""
}

// Close stops workers, terminates the active run if any, signals the event workflow to complete,
// waits for it to finish, then closes the Temporal client. Only one run can be active per agent.
func (a *Agent) Close() {
	a.logger.Info("closing agent", zap.String("name", a.Name))
	a.runMu.Lock()
	workflowID := a.activeRunWorkflowID
	eventWorkflowID := a.activeEventWorkflowID
	a.runMu.Unlock()

	if a.temporalClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if workflowID != "" {
			a.logger.Debug("terminating active run workflow", zap.String("workflowID", workflowID))
			_ = a.temporalClient.TerminateWorkflow(ctx, workflowID, "", "agent closed")
		}
		if eventWorkflowID != "" {
			a.logger.Debug("signaling event workflow to complete", zap.String("eventWorkflowID", eventWorkflowID))
			_ = a.temporalClient.SignalWorkflow(ctx, eventWorkflowID, "", eventWorkflowCompleteSignal, nil)
			// Wait for event workflow to complete gracefully (worker must stay running to process the signal)
			run := a.temporalClient.GetWorkflow(ctx, eventWorkflowID, "")
			_ = run.Get(ctx, nil)
		}
	}

	if a.localAgentWorker != nil {
		a.logger.Debug("stopping local agent worker")
		a.localAgentWorker.stop()
	}
	if a.eventWorker != nil {
		a.logger.Debug("stopping event worker")
		a.eventWorker.Stop()
	}
	if a.temporalClient != nil {
		a.temporalClient.Close()
	}
	a.logger.Info("agent closed", zap.String("name", a.Name))
}

// Run starts the agent workflow and returns the result. Handles approvals when
// tools require them. Use WithTimeout or a context with deadline to avoid blocking.
func (a *Agent) Run(ctx context.Context, input string) (*AgentResponse, error) {
	a.logger.Debug("run started", zap.String("agent", a.Name), zap.Int("inputLen", len(input)))
	runCtx := ctx
	d := a.timeout
	if d == 0 {
		d = defaultTimeout
	}
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	id := uuid.New().String()
	workflowID := fmt.Sprintf("%s-%s", a.Name, id)
	cleanup, err := a.beginRun(workflowID)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	wfInput := AgentWorkflowInput{
		Input:            input,
		StreamingEnabled: a.streamEnabled,
		EventWorkflowID:  "",
	}
	if a.enableRemoteWorkers {
		a.createEventWorker()
		var err error
		wfInput.EventWorkflowID, err = a.ensureEventWorkflowStarted(ctx)
		if err != nil {
			return nil, err
		}
	}

	var approvalCh <-chan *approvalRequest
	var closeApproval func() error
	if a.approvalHandler != nil {
		var err error
		approvalCh, closeApproval, err = a.subscribeToApprovals(runCtx, workflowID)
		if err != nil {
			a.logger.Error("failed to subscribe to approvals", zap.String("workflowID", workflowID), zap.Error(err))
			return nil, err
		}
		defer closeApproval()
	}

	hasWorkers := a.hasWorkers(ctx, a.taskQueue)
	if !hasWorkers {
		a.logger.Warn("no workers available on task queue", zap.String("taskQueue", a.taskQueue))
		return nil, fmt.Errorf("no workers available on task queue %s", a.taskQueue)
	}
	a.logger.Debug("workers available on task queue", zap.String("taskQueue", a.taskQueue))

	a.logger.Debug("executing agent workflow",
		zap.String("workflowID", workflowID),
		zap.Bool("streamingEnabled", wfInput.StreamingEnabled),
		zap.Bool("hasEventWorkflow", wfInput.EventWorkflowID != ""))

	wf := NewAgentWorkerDefault()
	workfowRun, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: a.taskQueue,
	}, wf.AgentWorkflow, wfInput)
	if err != nil {
		a.logger.Error("failed to execute agent workflow", zap.String("workflowID", workflowID), zap.Error(err))
		return nil, err
	}

	responseCh := make(chan *AgentResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		var response *AgentResponse
		err = workfowRun.Get(runCtx, &response)
		if err != nil {
			errCh <- err
			return
		}
		responseCh <- response
	}()

	type approvalResponse struct {
		toolName  string
		status    ApprovalStatus
		taskToken []byte
	}
	var approvalResponseCh chan approvalResponse
	if a.approvalHandler != nil {
		approvalResponseCh = make(chan approvalResponse, 16)
	}

	a.logger.Debug("waiting for agent workflow response", zap.String("workflowID", workflowID))

	for {
		select {
		case r := <-responseCh:
			a.logger.Debug("received agent workflow response",
				zap.String("agentName", r.AgentName),
				zap.String("model", r.Model),
				zap.Int("contentLen", len(r.Content)))
			return r, nil
		case err := <-errCh:
			a.logger.Error("agent workflow failed", zap.String("workflowID", workflowID), zap.Error(err))
			return nil, err
		case <-runCtx.Done():
			a.logger.Debug("run context cancelled", zap.String("workflowID", workflowID), zap.Error(runCtx.Err()))
			return nil, runCtx.Err()
		case req := <-approvalCh:
			if req == nil || req.AgentWorkflowID != workflowID {
				a.logger.Debug("ignoring approval request (nil or workflow ID mismatch)", zap.String("workflowID", workflowID))
				continue
			}
			a.logger.Debug("received approval request for tool", zap.String("tool", req.ToolName), zap.Int("argCount", len(req.Args)))
			taskToken := req.TaskToken
			onApproval := func(status ApprovalStatus) error {
				if status != ApprovalStatusRejected && status != ApprovalStatusApproved {
					return errors.New("invalid approval status")
				}
				approvalResponseCh <- approvalResponse{toolName: req.ToolName, taskToken: taskToken, status: status}
				return nil
			}
			a.approvalHandler(runCtx, &req.ApprovalRequest, onApproval)
		case resp := <-approvalResponseCh:
			a.logger.Debug("received approval response for tool", zap.String("tool", resp.toolName), zap.String("status", string(resp.status)))
			if err := a.temporalClient.CompleteActivity(runCtx, resp.taskToken, resp.status, nil); err != nil {
				a.logger.Error("failed to complete approval activity", zap.String("tool", resp.toolName), zap.Error(err))
				return nil, err
			}
		}
	}
}

// RunStream starts the run and returns a channel of AgentEvent. Events are streamed until
// AgentEventComplete. Handles approvals via ApprovalHandler when tools require them.
func (a *Agent) RunStream(ctx context.Context, input string) (chan *AgentEvent, error) {
	a.logger.Debug("run stream started", zap.String("agent", a.Name), zap.Int("inputLen", len(input)))
	runCtx := ctx
	d := a.timeout
	if d == 0 {
		d = defaultTimeout
	}
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	id := uuid.New().String()
	workflowID := fmt.Sprintf("%s-%s", a.Name, id)
	cleanup, err := a.beginRun(workflowID)
	if err != nil {
		return nil, err
	}

	var eventWorkflowID string
	if a.enableRemoteWorkers {
		a.createEventWorker()
		var err error
		eventWorkflowID, err = a.ensureEventWorkflowStarted(ctx)
		if err != nil {
			cleanup()
			return nil, err
		}
	}

	wfInput := AgentWorkflowInput{
		Input:            input,
		EventWorkflowID:  eventWorkflowID,
		StreamingEnabled: a.streamEnabled,
	}

	eventCh, closeEvent, err := a.subscribeToAgentEvents(runCtx, workflowID)
	if err != nil {
		a.logger.Error("failed to subscribe to agent events", zap.String("workflowID", workflowID), zap.Error(err))
		cleanup()
		return nil, err
	}
	a.logger.Debug("subscribed to agent events", zap.String("workflowID", workflowID))

	var approvalCh <-chan *approvalRequest
	var closeApproval func() error
	if a.approvalHandler != nil {
		approvalCh, closeApproval, err = a.subscribeToApprovals(runCtx, workflowID)
		if err != nil {
			a.logger.Error("failed to subscribe to approvals", zap.String("workflowID", workflowID), zap.Error(err))
			closeEvent()
			cleanup()
			return nil, err
		}
	}

	hasWorkers := a.hasWorkers(ctx, a.taskQueue)
	if !hasWorkers {
		a.logger.Warn("no workers available on task queue", zap.String("taskQueue", a.taskQueue))
		cleanup()
		return nil, fmt.Errorf("no workers available on task queue %s", a.taskQueue)
	}

	a.logger.Debug("executing agent workflow (stream)", zap.String("workflowID", workflowID))
	wf := NewAgentWorkerDefault()
	_, err = a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: a.taskQueue,
	}, wf.AgentWorkflow, wfInput)
	if err != nil {
		a.logger.Error("failed to execute agent workflow", zap.String("workflowID", workflowID), zap.Error(err))
		cleanup()
		closeEvent()
		if closeApproval != nil {
			closeApproval()
		}
		return nil, err
	}

	outCh := make(chan *AgentEvent, 64)
	go func() {
		defer close(outCh)
		defer closeEvent()
		if closeApproval != nil {
			defer closeApproval()
		}
		defer cleanup()
		for ev := range eventCh {
			outCh <- ev
			if ev != nil && ev.Type == AgentEventComplete {
				return
			}
		}
	}()

	if a.approvalHandler != nil {
		approvalResponseCh := make(chan struct {
			taskToken []byte
			status    ApprovalStatus
		}, 16)
		go func() {
			for req := range approvalCh {
				if req == nil || req.AgentWorkflowID != workflowID {
					continue
				}
				taskToken := req.TaskToken
				onApproval := func(status ApprovalStatus) error {
					if status != ApprovalStatusRejected && status != ApprovalStatusApproved {
						return errors.New("invalid approval status")
					}
					approvalResponseCh <- struct {
						taskToken []byte
						status    ApprovalStatus
					}{taskToken: taskToken, status: status}
					return nil
				}
				a.approvalHandler(runCtx, &req.ApprovalRequest, onApproval)
			}
		}()
		go func() {
			for resp := range approvalResponseCh {
				if err := a.temporalClient.CompleteActivity(runCtx, resp.taskToken, resp.status, nil); err != nil {
					a.logger.Error("failed to complete approval activity (stream)", zap.Error(err))
				}
			}
		}()
	}

	return outCh, nil
}
