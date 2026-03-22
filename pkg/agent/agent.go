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
	// workersCheckTimeout is how long hasWorkers polls for pollers before giving up.
	workersCheckTimeout = 15 * time.Second
)

// Agent runs LLM-backed workflows via Temporal. It holds agentConfig and client-side state
// (agentChannel, event worker). Uses AgentWorker for run workflow execution.
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

// ErrAgentAlreadyRunning is returned when Run, RunAsync, or RunStream is called while a run is already in progress.
var ErrAgentAlreadyRunning = errors.New("agent already has an active run")

// AgentResponse is the return value of AgentRun.Get / AgentWorkflow.
type AgentResponse struct {
	Content   string         `json:"content"`
	AgentName string         `json:"agent_name"`
	Model     string         `json:"model"`
	Metadata  map[string]any `json:"metadata"`
}

// RunAsyncResult is the single outcome from RunAsync. After the channel closes, Err is non-nil
// on failure; otherwise Response is non-nil.
type RunAsyncResult struct {
	Response *AgentResponse
	Err      error
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
	if a.disableWorker && a.streamEnabled && !a.enableRemoteWorkers {
		return nil, fmt.Errorf("DisableWorker with streaming requires WithEnableRemoteWorkers(true)")
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
	if a.temporalClient != nil && a.ownsTemporalClient {
		a.temporalClient.Close()
	}
	a.logger.Info("agent closed", zap.String("name", a.Name))
}

// Run starts the agent workflow and returns the result. Use WithApprovalHandler when tools require approval (Run only; handler uses req.Respond). RunStream uses AgentEventToolApproval + OnApproval.
// Use WithTimeout or a context with deadline to avoid blocking.
// When using WithConversation, pass the conversation ID (runtime id from user/session); agent and worker use the same ID.
func (a *Agent) Run(ctx context.Context, input string, conversationID string) (*AgentResponse, error) {
	a.logger.Debug("run started", zap.String("agent", a.Name), zap.Int("inputLen", len(input)))

	if err := a.validateConversationID(conversationID); err != nil {
		return nil, err
	}

	if a.hasApprovalTools() && a.approvalHandler == nil {
		return nil, fmt.Errorf("tools require approval but WithApprovalHandler was not set (required for Run)")
	}

	runCtx := ctx
	d := a.timeout
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
		UserPrompt:       input,
		StreamingEnabled: false,
		EventWorkflowID:  "",
		ConversationID:   conversationID,
		EventTypes:       []AgentEventType{},
	}
	if a.enableRemoteWorkers {
		a.createEventWorker()
		var err error
		wfInput.EventWorkflowID, err = a.ensureEventWorkflowStarted(ctx)
		if err != nil {
			return nil, err
		}
	}

	var eventCh <-chan *AgentEvent
	var closeEvent func() error
	if a.approvalHandler != nil {
		wfInput.EventTypes = []AgentEventType{AgentEventToolApproval}
		eventCh, closeEvent, err = a.subscribeToAgentEvents(runCtx, workflowID)
		if err != nil {
			a.logger.Error("failed to subscribe to agent events", zap.String("workflowID", workflowID), zap.Error(err))
			return nil, err
		}
		defer func() { _ = closeEvent() }()
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
		approvalToken string
		status        ApprovalStatus
	}
	approvalResponseCh := make(chan approvalResponse, 16)

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
			a.logger.Debug("run context cancelled, terminating agent workflow", zap.String("workflowID", workflowID), zap.Error(runCtx.Err()))
			termCtx, termCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer termCancel()
			if a.temporalClient != nil {
				_ = a.temporalClient.TerminateWorkflow(termCtx, workflowID, "", "run timeout")
			}
			return nil, runCtx.Err()
		case ev := <-eventCh:
			if ev == nil || ev.Type != AgentEventToolApproval || ev.Approval == nil || ev.Approval.ApprovalToken == "" {
				continue
			}
			approvalToken := ev.Approval.ApprovalToken
			onApproval := func(status ApprovalStatus) error {
				if status != ApprovalStatusRejected && status != ApprovalStatusApproved {
					return errors.New("invalid approval status")
				}
				approvalResponseCh <- approvalResponse{approvalToken: approvalToken, status: status}
				return nil
			}
			approvalCtx, cancel := context.WithTimeout(runCtx, a.approvalTimeout)
			a.approvalHandler(approvalCtx, &ApprovalRequest{
				ToolName: ev.Approval.ToolName,
				Args:     ev.Approval.Args,
				Respond:  onApproval,
			})
			cancel()
		case resp := <-approvalResponseCh:
			if err := a.OnApproval(runCtx, resp.approvalToken, resp.status); err != nil {
				a.logger.Error("failed to complete approval", zap.Error(err))
				return nil, err
			}
		}
	}
}

// RunAsync starts the run in a goroutine and returns two channels:
//   - resultCh: receives exactly one RunAsyncResult, then closes.
//   - approvalCh: receives each pending tool approval; call req.Respond. Channel closes when the run ends.
//
// For each approval, call req.Respond(Approved|Rejected) exactly once.
//
// WithApprovalHandler is temporarily replaced for the duration of the run; restore happens when the run finishes.
// If tools do not require approval, approvalCh is still closed immediately with no values.
func (a *Agent) RunAsync(ctx context.Context, input string, conversationID string) (resultCh <-chan RunAsyncResult, approvalCh <-chan *ApprovalRequest, err error) {
	a.logger.Debug("run async started", zap.String("agent", a.Name), zap.Int("inputLen", len(input)))

	if err := a.validateConversationID(conversationID); err != nil {
		return nil, nil, err
	}

	resCh := make(chan RunAsyncResult, 1)
	apprCh := make(chan *ApprovalRequest, 16)

	go func() {
		defer close(apprCh)
		defer close(resCh)

		var saved ApprovalHandler
		if a.hasApprovalTools() {
			saved = a.approvalHandler
			a.approvalHandler = func(handlerCtx context.Context, req *ApprovalRequest) {
				out := &ApprovalRequest{
					ToolName: req.ToolName,
					Args:     copyApprovalArgs(req.Args),
					Respond:  req.Respond,
				}
				select {
				case apprCh <- out:
				default:
					// Avoid blocking Run's event loop if consumer is slow.
					go func(p *ApprovalRequest) { apprCh <- p }(out)
				}
			}
			defer func() { a.approvalHandler = saved }()
		}

		resp, runErr := a.Run(ctx, input, conversationID)
		if runErr != nil {
			resCh <- RunAsyncResult{Err: runErr}
			return
		}
		resCh <- RunAsyncResult{Response: resp}
	}()

	return resCh, apprCh, nil
}

func copyApprovalArgs(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// RunStream starts the run and returns a channel of AgentEvent. Events are streamed until
// AgentEventComplete. For tool approvals, receive AgentEventToolApproval and call OnApproval
// as in the streaming examples.
// When using WithConversation, pass the conversation ID.
func (a *Agent) RunStream(ctx context.Context, input string, conversationID string) (chan *AgentEvent, error) {
	a.logger.Debug("run stream started", zap.String("agent", a.Name), zap.Int("inputLen", len(input)))

	if err := a.validateConversationID(conversationID); err != nil {
		return nil, err
	}

	id := uuid.New().String()
	workflowID := fmt.Sprintf("%s-%s", a.Name, id)
	cleanup, err := a.beginRun(workflowID)
	if err != nil {
		return nil, err
	}
	streamStarted := false
	defer func() {
		if !streamStarted {
			cleanup()
		}
	}()

	var eventWorkflowID string
	if a.enableRemoteWorkers {
		a.createEventWorker()
		var err error
		eventWorkflowID, err = a.ensureEventWorkflowStarted(ctx)
		if err != nil {
			return nil, err
		}
	}

	wfInput := AgentWorkflowInput{
		UserPrompt:       input,
		EventWorkflowID:  eventWorkflowID,
		StreamingEnabled: a.streamEnabled,
		ConversationID:   conversationID,
		EventTypes:       []AgentEventType{AgentEventAll},
	}

	runCtx := ctx
	var runCancel context.CancelFunc
	d := a.timeout
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		runCtx, runCancel = context.WithTimeout(ctx, d)
	}
	defer func() {
		if !streamStarted && runCancel != nil {
			runCancel()
		}
	}()

	eventCh, closeEvent, err := a.subscribeToAgentEvents(runCtx, workflowID)
	if err != nil {
		a.logger.Error("failed to subscribe to agent events", zap.String("workflowID", workflowID), zap.Error(err))
		return nil, err
	}
	a.logger.Debug("subscribed to agent events", zap.String("workflowID", workflowID))
	defer func() {
		if !streamStarted && closeEvent != nil {
			_ = closeEvent()
		}
	}()

	hasWorkers := a.hasWorkers(ctx, a.taskQueue)
	if !hasWorkers {
		a.logger.Warn("no workers available on task queue", zap.String("taskQueue", a.taskQueue))
		return nil, fmt.Errorf("no workers available on task queue %s", a.taskQueue)
	}

	a.logger.Debug("executing agent workflow (stream)", zap.String("workflowID", workflowID))
	wf := NewAgentWorkerDefault()
	workflowRun, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: a.taskQueue,
	}, wf.AgentWorkflow, wfInput)
	if err != nil {
		a.logger.Error("failed to execute agent workflow", zap.String("workflowID", workflowID), zap.Error(err))
		return nil, err
	}

	streamStarted = true
	outCh := make(chan *AgentEvent, 64)
	wfErrCh := make(chan error, 1)
	go func() {
		var result AgentResponse
		if err := workflowRun.Get(runCtx, &result); err != nil {
			wfErrCh <- err
		}
	}()
	go func() {
		defer close(outCh)
		defer func() { _ = closeEvent() }()
		defer cleanup()
		if runCancel != nil {
			defer runCancel()
		}
		for {
			select {
			case <-runCtx.Done():
				termCtx, termCancel := context.WithTimeout(context.Background(), 15*time.Second)
				if a.temporalClient != nil {
					_ = a.temporalClient.TerminateWorkflow(termCtx, workflowID, "", "run timeout")
				}
				termCancel()
				outCh <- &AgentEvent{
					Type:      AgentEventError,
					Content:   "request timed out (approval expired or deadline exceeded)",
					Timestamp: time.Now(),
				}
				return
			case wfErr := <-wfErrCh:
				a.logger.Error("agent workflow failed (stream)", zap.String("workflowID", workflowID), zap.Error(wfErr))
				outCh <- &AgentEvent{
					Type:      AgentEventError,
					Content:   wfErr.Error(),
					Timestamp: time.Now(),
				}
				return
			case ev, ok := <-eventCh:
				if !ok {
					return
				}
				if ev == nil {
					continue
				}
				outCh <- ev
				if ev.Type == AgentEventComplete {
					return
				}
			}
		}
	}()

	return outCh, nil
}

// copyEventWithShortApprovalToken builds an event copy with a substituted approval identifier.
func copyEventWithShortApprovalToken(ev *AgentEvent, shortToken string) *AgentEvent {
	c := *ev
	if c.Approval != nil {
		c.Approval = &ToolApprovalEvent{
			ToolCallID:    ev.Approval.ToolCallID,
			ToolName:      ev.Approval.ToolName,
			Args:          ev.Approval.Args,
			ApprovalToken: shortToken,
		}
	}
	return &c
}

func (a *Agent) validateConversationID(conversationID string) error {
	if conversationID != "" && a.conversation == nil {
		return fmt.Errorf("conversationID %s requires conversation configuration", conversationID)
	}
	if conversationID == "" && a.conversation != nil {
		return fmt.Errorf("conversationID is required when using conversation")
	}
	return nil
}
