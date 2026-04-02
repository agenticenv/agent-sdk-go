package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
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
	eventbus              eventbus.EventBus
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
		agentConfig: *cfg,
		eventbus:    eventbus.NewInmem(cfg.logger),
	}
	if a.disableLocalWorker && a.streamEnabled && !a.enableRemoteWorkers {
		return nil, fmt.Errorf("DisableLocalWorker with streaming requires EnableRemoteWorkers()")
	}

	if !a.disableLocalWorker {
		a.localAgentWorker = newAgentWorkerFromConfig(cfg, a.eventbus)
	}
	// Sub-agents must share the parent's in-memory pub/sub so Run/RunStream subscribers on the main run receive
	// delegation events and approvals from specialist workflows in the same process.
	for _, sub := range a.subAgents {
		if sub != nil {
			wireInMemoryEventChannelToSubAgents(a, sub)
		}
	}

	return a, nil
}

func wireInMemoryEventChannelToSubAgents(root, agent *Agent) {
	if agent == nil || root == nil || root.eventbus == nil {
		return
	}
	agent.eventbus = root.eventbus
	if agent.localAgentWorker != nil {
		agent.localAgentWorker.eventbus = root.eventbus
	}
	for _, child := range agent.subAgents {
		wireInMemoryEventChannelToSubAgents(root, child)
	}
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
			a.logger.Debug(ctx, "workers found on task queue", slog.String("taskQueue", q), slog.Int("pollers", len(res.GetPollers())))
			return true
		}
		if time.Now().After(deadlineTime) {
			a.logger.Debug(ctx, "workers check timed out", slog.String("taskQueue", q))
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
		a.logger.Debug(context.Background(), "event worker already running", slog.String("taskQueue", a.taskQueue))
		return
	}
	eventQueue := getEventTaskQueue(a.taskQueue)
	a.logger.Info(context.Background(), "starting event worker", slog.String("taskQueue", eventQueue))
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
	a.logger.Info(context.Background(), "agent created", slog.String("name", a.Name), slog.String("taskQueue", a.taskQueue), slog.Bool("embedWorker", a.localAgentWorker != nil))
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
		a.logger.Debug(ctx, "reusing existing event workflow", slog.String("eventWorkflowID", id))
		return id, nil
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.activeEventWorkflowID != "" {
		return a.activeEventWorkflowID, nil
	}

	eventWorkflowID := a.getEventWorkflowID()
	a.logger.Debug(ctx, "executing event workflow", slog.String("eventWorkflowID", eventWorkflowID))

	_, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       eventWorkflowID,
		TaskQueue:                getEventTaskQueue(a.taskQueue),
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, a.AgentEventWorkflow)
	if err != nil {
		a.logger.Error(ctx, "failed to start event workflow", slog.String("eventWorkflowID", eventWorkflowID), slog.Any("error", err))
		return "", err
	}
	a.activeEventWorkflowID = eventWorkflowID
	a.logger.Info(ctx, "event workflow started", slog.String("eventWorkflowID", eventWorkflowID))
	return eventWorkflowID, nil
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
		a.logger.Debug(context.Background(), "beginRun rejected: already running", slog.String("active", a.activeRunWorkflowID), slog.String("requested", workflowID))
		return nil, ErrAgentAlreadyRunning
	}
	a.activeRunWorkflowID = workflowID
	a.logger.Debug(context.Background(), "beginRun", slog.String("workflowID", workflowID))
	return func() { a.endRun() }, nil
}

// endRun clears the active run state when a run completes. Does not clear activeEventWorkflowID (per-agent).
func (a *Agent) endRun() {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.activeRunWorkflowID != "" {
		a.logger.Debug(context.Background(), "endRun", slog.String("workflowID", a.activeRunWorkflowID))
	}
	a.activeRunWorkflowID = ""
}

// Close stops workers, terminates the active run if any, signals the event workflow to complete,
// waits for it to finish, then closes the Temporal client. Only one run can be active per agent.
func (a *Agent) Close() {
	a.logger.Info(context.Background(), "closing agent", slog.String("name", a.Name))
	a.runMu.Lock()
	workflowID := a.activeRunWorkflowID
	eventWorkflowID := a.activeEventWorkflowID
	a.runMu.Unlock()

	if a.temporalClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if workflowID != "" {
			a.logger.Debug(ctx, "terminating active run workflow", slog.String("workflowID", workflowID))
			_ = a.temporalClient.TerminateWorkflow(ctx, workflowID, "", "agent closed")
		}
		if eventWorkflowID != "" {
			a.logger.Debug(ctx, "signaling event workflow to complete", slog.String("eventWorkflowID", eventWorkflowID))
			_ = a.temporalClient.SignalWorkflow(ctx, eventWorkflowID, "", eventWorkflowCompleteSignal, nil)
			// Wait for event workflow to complete gracefully (worker must stay running to process the signal)
			run := a.temporalClient.GetWorkflow(ctx, eventWorkflowID, "")
			_ = run.Get(ctx, nil)
		}
	}

	if a.localAgentWorker != nil {
		a.logger.Debug(context.Background(), "stopping local agent worker")
		a.localAgentWorker.stop()
	}
	if a.eventWorker != nil {
		a.logger.Debug(context.Background(), "stopping event worker")
		a.eventWorker.Stop()
	}
	if a.temporalClient != nil && a.ownsTemporalClient {
		a.temporalClient.Close()
	}
	a.logger.Info(context.Background(), "agent closed", slog.String("name", a.Name))
}

// Run starts the agent workflow and returns the result. Use WithApprovalHandler when tools require approval (Run only; handler uses req.Respond). RunStream uses AgentEventApproval + OnApproval.
// Use WithTimeout or a context with deadline to avoid blocking.
// When using WithConversation, pass the conversation ID (runtime id from user/session); agent and worker use the same ID.
func (a *Agent) Run(ctx context.Context, input string, conversationID string) (*AgentResponse, error) {
	a.logger.Debug(ctx, "run started", slog.String("agent", a.Name), slog.Int("inputLen", len(input)))

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

	workflowID := a.getWorkflowID(false)
	cleanup, err := a.beginRun(workflowID)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	wfInput := AgentWorkflowInput{
		UserPrompt:       input,
		StreamingEnabled: false,
		EventWorkflowID:  "",
		LocalChannelName: eventChannelName(workflowID),
		ConversationID:   conversationID,
		EventTypes:       []AgentEventType{},
		SubAgentDepth:    0,
		SubAgentRoutes:   a.buildSubAgentRoutes(),
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
		wfInput.EventTypes = []AgentEventType{AgentEventApproval}
		eventCh, closeEvent, err = a.subscribeToAgentEvents(runCtx, wfInput.LocalChannelName)
		if err != nil {
			a.logger.Error(runCtx, "failed to subscribe to agent events", slog.String("workflowID", workflowID), slog.Any("error", err))
			return nil, err
		}
		defer func() { _ = closeEvent() }()
	}

	hasWorkers := a.hasWorkers(ctx, a.taskQueue)
	if !hasWorkers {
		a.logger.Warn(runCtx, "no workers available on task queue", slog.String("taskQueue", a.taskQueue))
		return nil, fmt.Errorf("no workers available on task queue %s", a.taskQueue)
	}
	a.logger.Debug(runCtx, "workers available on task queue", slog.String("taskQueue", a.taskQueue))

	a.logger.Debug(runCtx, "executing agent workflow",
		slog.String("workflowID", workflowID),
		slog.Bool("streamingEnabled", wfInput.StreamingEnabled),
		slog.Bool("hasEventWorkflow", wfInput.EventWorkflowID != ""))

	wf := NewAgentWorkerDefault()
	workfowRun, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: a.taskQueue,
	}, wf.AgentWorkflow, wfInput)
	if err != nil {
		a.logger.Error(runCtx, "failed to execute agent workflow", slog.String("workflowID", workflowID), slog.Any("error", err))
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

	a.logger.Debug(runCtx, "waiting for agent workflow response", slog.String("workflowID", workflowID))

	for {
		select {
		case r := <-responseCh:
			a.logger.Debug(runCtx, "received agent workflow response",
				slog.String("agentName", r.AgentName),
				slog.String("model", r.Model),
				slog.Int("contentLen", len(r.Content)))
			return r, nil
		case err := <-errCh:
			a.logger.Error(runCtx, "agent workflow failed", slog.String("workflowID", workflowID), slog.Any("error", err))
			return nil, err
		case <-runCtx.Done():
			a.logger.Debug(runCtx, "run context cancelled, terminating agent workflow", slog.String("workflowID", workflowID), slog.Any("error", runCtx.Err()))
			termCtx, termCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer termCancel()
			if a.temporalClient != nil {
				_ = a.temporalClient.TerminateWorkflow(termCtx, workflowID, "", "run timeout")
			}
			return nil, runCtx.Err()
		case ev := <-eventCh:
			if ev == nil || ev.Type != AgentEventApproval || ev.Approval == nil || ev.Approval.ApprovalToken == "" {
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
				ToolName:       ev.Approval.ToolName,
				Args:           ev.Approval.Args,
				Respond:        onApproval,
				Kind:           ev.Approval.Kind,
				AgentName:      ev.AgentName,
				DelegateToName: ev.Approval.DelegateToName,
			})
			cancel()
		case resp := <-approvalResponseCh:
			if err := a.OnApproval(runCtx, resp.approvalToken, resp.status); err != nil {
				a.logger.Error(runCtx, "failed to complete approval", slog.Any("error", err))
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
	a.logger.Debug(ctx, "run async started", slog.String("agent", a.Name), slog.Int("inputLen", len(input)))

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
					ToolName:       req.ToolName,
					Args:           copyApprovalArgs(req.Args),
					Respond:        req.Respond,
					Kind:           req.Kind,
					AgentName:      req.AgentName,
					DelegateToName: req.DelegateToName,
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
// AgentEventComplete from this agent (the root of the run). Complete events from delegated
// sub-agents are still delivered but do not close the stream. After that root complete, the
// channel stays open until the root workflow run finishes on Temporal (there is often more
// work after the event, e.g. post-sub-agent activities), then closes.
// For approvals (tool or delegation), receive AgentEventApproval and call OnApproval as in the streaming examples.
// When using WithConversation, pass the conversation ID.
func (a *Agent) RunStream(ctx context.Context, input string, conversationID string) (chan *AgentEvent, error) {
	a.logger.Debug(ctx, "run stream started", slog.String("agent", a.Name), slog.Int("inputLen", len(input)))

	if err := a.validateConversationID(conversationID); err != nil {
		return nil, err
	}

	workflowID := a.getWorkflowID(true)
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
		LocalChannelName: eventChannelName(workflowID),
		StreamingEnabled: a.streamEnabled,
		ConversationID:   conversationID,
		EventTypes:       []AgentEventType{agentEventAll},
		SubAgentDepth:    0,
		SubAgentRoutes:   a.buildSubAgentRoutes(),
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

	eventCh, closeEvent, err := a.subscribeToAgentEvents(runCtx, wfInput.LocalChannelName)
	if err != nil {
		a.logger.Error(runCtx, "failed to subscribe to agent events", slog.String("channel", wfInput.LocalChannelName), slog.Any("error", err))
		return nil, err
	}
	a.logger.Debug(runCtx, "subscribed to agent events", slog.String("channel", wfInput.LocalChannelName))
	defer func() {
		if !streamStarted && closeEvent != nil {
			_ = closeEvent()
		}
	}()

	hasWorkers := a.hasWorkers(ctx, a.taskQueue)
	if !hasWorkers {
		a.logger.Warn(runCtx, "no workers available on task queue", slog.String("taskQueue", a.taskQueue))
		return nil, fmt.Errorf("no workers available on task queue %s", a.taskQueue)
	}

	a.logger.Debug(runCtx, "executing agent workflow (stream)", slog.String("workflowID", workflowID))
	wf := NewAgentWorkerDefault()
	workflowRun, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: a.taskQueue,
	}, wf.AgentWorkflow, wfInput)
	if err != nil {
		a.logger.Error(runCtx, "failed to execute agent workflow", slog.String("workflowID", workflowID), slog.Any("error", err))
		return nil, err
	}

	streamStarted = true
	outCh := make(chan *AgentEvent, 64)
	wfErrCh := make(chan error, 1)
	var getWG sync.WaitGroup
	getWG.Add(1)
	go func() {
		defer getWG.Done()
		// cleanup/endRun only after Get returns. runCtx must stay valid until then so the workflow
		// can finish (after root complete, work remains: e.g. SendAgentEventUpdateActivity—longer
		// when a sub-agent child workflow just ran).
		defer cleanup()
		defer func() {
			if runCancel != nil {
				runCancel()
			}
		}()
		var result AgentResponse
		if err := workflowRun.Get(runCtx, &result); err != nil {
			wfErrCh <- err
		}
	}()
	go func() {
		defer close(outCh)
		defer func() { _ = closeEvent() }()
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
				a.logger.Error(runCtx, "agent workflow failed (stream)", slog.String("workflowID", workflowID), slog.Any("error", wfErr))
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
				if streamCompleteEndsRun(ev, a.Name) {
					// Root complete is emitted before the workflow returns; after sub-agent delegation
					// there is often more server-side work. Wait for Get+cleanup so the run reaches
					// Completed before we close the channel (avoids Close() terminating a live run).
					getWG.Wait()
					return
				}
			}
		}
	}()

	return outCh, nil
}

// streamCompleteEndsRun is true when this complete event should end RunStream for the root agent.
// Sub-agent workflows emit AgentEventComplete too; those must not close the root stream.
// Empty AgentName counts as root for backward compatibility.
func streamCompleteEndsRun(ev *AgentEvent, rootName string) bool {
	if ev == nil || ev.Type != AgentEventComplete {
		return false
	}
	src := strings.TrimSpace(ev.AgentName)
	root := strings.TrimSpace(rootName)
	if src == "" {
		return true
	}
	return src == root
}

// copyEventWithShortApprovalToken builds an event copy with a substituted approval identifier.
func copyEventWithShortApprovalToken(ev *AgentEvent, shortToken string) *AgentEvent {
	c := *ev
	if c.Approval != nil {
		c.Approval = &ApprovalEvent{
			ToolCallID:     ev.Approval.ToolCallID,
			ToolName:       ev.Approval.ToolName,
			Args:           ev.Approval.Args,
			ApprovalToken:  shortToken,
			Kind:           ev.Approval.Kind,
			DelegateToName: ev.Approval.DelegateToName,
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

func (a *Agent) getWorkflowID(isStream bool) string {
	id := uuid.New().String()
	if isStream {
		return fmt.Sprintf("agent-stream-%s-%s", a.Name, id)
	}
	return fmt.Sprintf("agent-run-%s-%s", a.Name, id)
}

func (a *Agent) getEventWorkflowID() string {
	id := uuid.New().String()
	return fmt.Sprintf("agent-event-%s-%s", a.Name, id)
}

// buildSubAgentRoutes snapshots sub-agent tool names, task queues, and nested routes for workflow input.
func (a *Agent) buildSubAgentRoutes() map[string]SubAgentRoute {
	if a == nil || len(a.subAgents) == 0 {
		return nil
	}
	out := make(map[string]SubAgentRoute, len(a.subAgents))
	for _, sub := range a.subAgents {
		if sub == nil {
			continue
		}
		tq := strings.TrimSpace(sub.taskQueue)
		if tq == "" {
			continue
		}
		name := SubAgentToolName(sub)
		out[name] = SubAgentRoute{
			TaskQueue:   tq,
			ChildRoutes: sub.buildSubAgentRoutes(),
		}
	}
	if len(out) == 0 {
		return nil
	}
	if a.logger != nil {
		names := make([]string, 0, len(out))
		for k := range out {
			names = append(names, k)
		}
		sort.Strings(names)
		a.logger.Debug(context.Background(), "built sub-agent routes for workflow input",
			slog.Any("subAgentToolNames", names),
			slog.Int("routeCount", len(out)))
	}
	return out
}
