package temporal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

var _ runtime.Runtime = (*TemporalRuntime)(nil)

const (
	// workersCheckTimeout is how long hasWorkers polls for pollers before giving up.
	workersCheckTimeout = 15 * time.Second
)

// ErrAgentAlreadyRunning is returned when Run, RunAsync, or RunStream is called while a run is already in progress.
var ErrAgentAlreadyRunning = errors.New("agent already has an active run")

type TemporalRuntime struct {
	TemporalRuntimeConfig

	eventbus              eventbus.EventBus
	runMu                 sync.Mutex
	activeRunWorkflowID   string
	activeEventWorkflowID string

	eventWorker   worker.Worker
	eventWorkerMu sync.Mutex

	agentWorker   worker.Worker
	agentWorkerMu sync.Mutex
}

func NewTemporalRuntime(opts ...Option) (*TemporalRuntime, error) {
	cfg, err := buildTemporalRuntimeConfig(opts...)
	if err != nil {
		return nil, err
	}
	cfg.logger.Info(context.Background(), "temporal runtime created", slog.String("name", cfg.agentName), slog.String("taskQueue", cfg.taskQueue))
	return &TemporalRuntime{
		TemporalRuntimeConfig: *cfg,
		eventbus:              eventbus.NewInmem(cfg.logger),
	}, nil
}

// override the event bus for the runtime.
// subagents runtime are updated with main agent's event bus.
func (rt *TemporalRuntime) SetEventBus(eventbus eventbus.EventBus) {
	rt.eventbus = eventbus
}

// GetEventBus returns the event bus for the runtime.
func (rt *TemporalRuntime) GetEventBus() eventbus.EventBus {
	return rt.eventbus
}

// Start starts the worker (blocks until Stop is called).
func (rt *TemporalRuntime) Start(ctx context.Context) error {
	rt.logger.Info(ctx, "starting temporal runtime worker", slog.String("taskQueue", rt.taskQueue))
	// createAgentWorker creates and registers a Temporal worker for the agent's run workflow and activities.

	rt.agentWorkerMu.Lock()
	defer rt.agentWorkerMu.Unlock()
	if rt.agentWorker != nil {
		rt.logger.Debug(ctx, "temporal runtime worker already running", slog.String("taskQueue", rt.taskQueue))
		return nil
	}
	rt.logger.Debug(ctx, "registering agent workflows and activities to temporal runtime worker", slog.String("taskQueue", rt.taskQueue))
	w := worker.New(rt.temporalClient, rt.taskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(rt.AgentWorkflow, workflow.RegisterOptions{Name: "AgentWorkflow"})
	w.RegisterActivityWithOptions(rt.AgentLLMActivity, activity.RegisterOptions{Name: "AgentLLMActivity"})
	w.RegisterActivityWithOptions(rt.AgentLLMStreamActivity, activity.RegisterOptions{Name: "AgentLLMStreamActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolApprovalActivity, activity.RegisterOptions{Name: "AgentToolApprovalActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolExecuteActivity, activity.RegisterOptions{Name: "AgentToolExecuteActivity"})
	w.RegisterActivityWithOptions(rt.SendAgentEventUpdateActivity, activity.RegisterOptions{Name: "SendAgentEventUpdateActivity"})
	w.RegisterActivityWithOptions(rt.AddConversationMessagesActivity, activity.RegisterOptions{Name: "AddConversationMessagesActivity"})
	rt.agentWorker = w
	err := rt.agentWorker.Start()
	if err != nil {
		rt.logger.Error(ctx, "failed to start temporal runtime worker", slog.String("taskQueue", rt.taskQueue), slog.Any("error", err))
		return err
	}
	rt.logger.Debug(ctx, "temporal runtime worker started", slog.String("taskQueue", rt.taskQueue))
	return nil
}

// Stop stops the worker. Unexported; Agent calls it when closing embedded worker.
func (rt *TemporalRuntime) Stop() {
	ctx := context.Background()
	if rt.remoteWorker {
		rt.logger.Debug(ctx, "stopping temporal runtime", slog.String("taskQueue", rt.taskQueue))
		if rt.agentWorker != nil {
			rt.logger.Debug(ctx, "stopping runtime remote worker", slog.String("taskQueue", rt.taskQueue))
			rt.agentWorker.Stop()
		}
		if rt.temporalClient != nil && rt.ownsTemporalClient {
			rt.logger.Debug(ctx, "closing remote worker temporal client", slog.String("taskQueue", rt.taskQueue))
			rt.temporalClient.Close()
		}
		rt.logger.Debug(ctx, "temporal runtime remote worker stopped", slog.String("taskQueue", rt.taskQueue))
	} else {
		rt.logger.Debug(ctx, "skipping temporal runtime stop for local worker", slog.String("taskQueue", rt.taskQueue))
	}
}

func (rt *TemporalRuntime) Close() {
	rt.logger.Info(context.Background(), "closing temporal runtime", slog.String("name", rt.agentName))

	rt.runMu.Lock()
	workflowID := rt.activeRunWorkflowID
	eventWorkflowID := rt.activeEventWorkflowID
	rt.runMu.Unlock()

	ctx := context.Background()

	if rt.temporalClient != nil {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if workflowID != "" {
			rt.logger.Debug(ctx, "terminating active run workflow", slog.String("workflowID", workflowID))
			_ = rt.temporalClient.TerminateWorkflow(ctx, workflowID, "", "agent closed")
		}
		if eventWorkflowID != "" {
			rt.logger.Debug(ctx, "signaling event workflow to complete", slog.String("eventWorkflowID", eventWorkflowID))
			_ = rt.temporalClient.SignalWorkflow(ctx, eventWorkflowID, "", eventWorkflowCompleteSignal, nil)
			// Wait for event workflow to complete gracefully (worker must stay running to process the signal)
			run := rt.temporalClient.GetWorkflow(ctx, eventWorkflowID, "")
			_ = run.Get(ctx, nil)
		}
	}

	if rt.eventWorker != nil {
		rt.logger.Debug(ctx, "stopping event worker on temporal runtime close")
		rt.eventWorker.Stop()
	}

	if rt.agentWorker != nil {
		rt.logger.Debug(ctx, "stopping runtime local worker", slog.String("taskQueue", rt.taskQueue))
		rt.agentWorker.Stop()
	}

	if rt.temporalClient != nil && rt.ownsTemporalClient {
		rt.logger.Debug(ctx, "closing temporal client on temporal runtime close")
		rt.temporalClient.Close()
	}
	rt.logger.Info(ctx, "temporal runtime closed", slog.String("name", rt.agentName))
}

func (rt *TemporalRuntime) Approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	if status != types.ApprovalStatusApproved && status != types.ApprovalStatusRejected {
		return fmt.Errorf("invalid approval status: %s", status)
	}
	taskToken, err := base64.StdEncoding.DecodeString(approvalToken)
	if err != nil {
		return fmt.Errorf("invalid approval token: %w", err)
	}
	return rt.temporalClient.CompleteActivity(ctx, taskToken, status, nil)
}

func (rt *TemporalRuntime) Run(ctx context.Context, req *runtime.RunRequest) (*types.AgentResponse, error) {
	rt.logger.Debug(ctx, "temporal runtime run started", slog.String("agent", req.AgentName), slog.Int("inputLen", len(req.UserPrompt)))

	runCtx := ctx
	d := rt.timeout
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	workflowID := rt.getWorkflowID(req.AgentName, false)

	cleanup, err := rt.beginRun(workflowID)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	wfInput := AgentWorkflowInput{
		UserPrompt:       req.UserPrompt,
		StreamingEnabled: false,
		EventWorkflowID:  "",
		LocalChannelName: eventChannelName(workflowID),
		ConversationID:   req.ConversationID,
		EventTypes:       []types.AgentEventType{},
		SubAgentDepth:    0,
		SubAgentRoutes:   req.SubAgentRoutes,
		MaxSubAgentDepth: req.MaxSubAgentDepth,
	}

	if req.EnableRemoteWorkers {
		rt.createEventWorker()
		var err error
		wfInput.EventWorkflowID, err = rt.ensureEventWorkflowStarted(runCtx, req.AgentName)
		if err != nil {
			return nil, err
		}
	}

	var eventCh <-chan *types.AgentEvent
	var closeEvent func() error
	if req.ApprovalHandler != nil {
		wfInput.EventTypes = []types.AgentEventType{types.AgentEventApproval}
		eventCh, closeEvent, err = rt.subscribeToAgentEvents(runCtx, wfInput.LocalChannelName)
		if err != nil {
			rt.logger.Error(runCtx, "failed to subscribe to agent events", slog.String("workflowID", workflowID), slog.Any("error", err))
			return nil, err
		}
		defer func() { _ = closeEvent() }()
	}

	hasWorkers := rt.hasWorkers(runCtx, rt.taskQueue)
	if !hasWorkers {
		rt.logger.Warn(runCtx, "no workers available on task queue", slog.String("taskQueue", rt.taskQueue))
		return nil, fmt.Errorf("no workers available on task queue %s", rt.taskQueue)
	}
	rt.logger.Debug(runCtx, "workers available on task queue", slog.String("taskQueue", rt.taskQueue))

	rt.logger.Debug(runCtx, "executing agent workflow",
		slog.String("workflowID", workflowID),
		slog.Bool("streamingEnabled", wfInput.StreamingEnabled),
		slog.Bool("hasEventWorkflow", wfInput.EventWorkflowID != ""))

	workfowRun, err := rt.temporalClient.ExecuteWorkflow(runCtx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: rt.taskQueue,
	}, rt.AgentWorkflow, wfInput)
	if err != nil {
		rt.logger.Error(runCtx, "failed to execute agent workflow", slog.String("workflowID", workflowID), slog.Any("error", err))
		return nil, err
	}

	resultCh := make(chan *types.AgentResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		var result *types.AgentResponse
		err = workfowRun.Get(runCtx, &result)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	type approvalResponse struct {
		approvalToken string
		status        types.ApprovalStatus
	}
	approvalResponseCh := make(chan approvalResponse, 16)

	rt.logger.Debug(runCtx, "waiting for agent workflow response", slog.String("workflowID", workflowID))

	for {
		select {
		case r := <-resultCh:
			rt.logger.Debug(runCtx, "received agent workflow response",
				slog.String("agentName", r.AgentName),
				slog.String("model", r.Model),
				slog.Int("contentLen", len(r.Content)))
			return r, nil
		case err := <-errCh:
			rt.logger.Error(runCtx, "agent workflow failed", slog.String("workflowID", workflowID), slog.Any("error", err))
			return nil, err
		case <-runCtx.Done():
			rt.logger.Debug(runCtx, "run context cancelled, terminating agent workflow", slog.String("workflowID", workflowID), slog.Any("error", runCtx.Err()))
			termCtx, termCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer termCancel()
			if rt.temporalClient != nil {
				_ = rt.temporalClient.TerminateWorkflow(termCtx, workflowID, "", "run timeout")
			}
			return nil, runCtx.Err()
		case ev := <-eventCh:
			if ev == nil || ev.Type != types.AgentEventApproval || ev.Approval == nil || ev.Approval.ApprovalToken == "" {
				continue
			}
			approvalToken := ev.Approval.ApprovalToken
			onApproval := func(status types.ApprovalStatus) error {
				if status != types.ApprovalStatusRejected && status != types.ApprovalStatusApproved {
					return errors.New("invalid approval status")
				}
				approvalResponseCh <- approvalResponse{approvalToken: approvalToken, status: status}
				return nil
			}
			approvalCtx, cancel := context.WithTimeout(runCtx, rt.approvalTimeout)
			req.ApprovalHandler(approvalCtx, &types.ApprovalRequest{
				ToolName:     ev.Approval.ToolName,
				Args:         ev.Approval.Args,
				Respond:      onApproval,
				Kind:         ev.Approval.Kind,
				AgentName:    ev.AgentName,
				SubAgentName: ev.Approval.SubAgentName,
			})
			cancel()
		case resp := <-approvalResponseCh:
			if err := rt.OnApproval(runCtx, resp.approvalToken, resp.status); err != nil {
				rt.logger.Error(runCtx, "failed to complete approval", slog.Any("error", err))
				return nil, err
			}
		}
	}
}

func (rt *TemporalRuntime) RunStream(ctx context.Context, req *runtime.RunRequest) (chan *types.AgentEvent, error) {
	rt.logger.Debug(ctx, "temporal runtime run stream started", slog.String("agent", req.AgentName), slog.Int("inputLen", len(req.UserPrompt)))

	workflowID := rt.getWorkflowID(req.AgentName, true)
	cleanup, err := rt.beginRun(workflowID)
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
	if req.EnableRemoteWorkers {
		rt.createEventWorker()
		var err error
		eventWorkflowID, err = rt.ensureEventWorkflowStarted(ctx, req.AgentName)
		if err != nil {
			return nil, err
		}
	}

	wfInput := AgentWorkflowInput{
		UserPrompt:       req.UserPrompt,
		EventWorkflowID:  eventWorkflowID,
		LocalChannelName: eventChannelName(workflowID),
		StreamingEnabled: req.StreamingEnabled,
		ConversationID:   req.ConversationID,
		EventTypes:       []types.AgentEventType{types.AgentEventAll},
		SubAgentDepth:    0,
		SubAgentRoutes:   req.SubAgentRoutes,
	}

	runCtx := ctx
	var runCancel context.CancelFunc
	d := rt.timeout
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		runCtx, runCancel = context.WithTimeout(ctx, d)
	}
	defer func() {
		if !streamStarted && runCancel != nil {
			runCancel()
		}
	}()

	eventCh, closeEvent, err := rt.subscribeToAgentEvents(runCtx, wfInput.LocalChannelName)
	if err != nil {
		rt.logger.Error(runCtx, "failed to subscribe to agent events", slog.String("channel", wfInput.LocalChannelName), slog.Any("error", err))
		return nil, err
	}
	rt.logger.Debug(runCtx, "subscribed to agent events", slog.String("channel", wfInput.LocalChannelName))
	defer func() {
		if !streamStarted && closeEvent != nil {
			_ = closeEvent()
		}
	}()

	hasWorkers := rt.hasWorkers(ctx, rt.taskQueue)
	if !hasWorkers {
		rt.logger.Warn(runCtx, "no workers available on task queue", slog.String("taskQueue", rt.taskQueue))
		return nil, fmt.Errorf("no workers available on task queue %s", rt.taskQueue)
	}

	rt.logger.Debug(runCtx, "executing agent workflow (stream)", slog.String("workflowID", workflowID))

	workflowRun, err := rt.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: rt.taskQueue,
	}, rt.AgentWorkflow, wfInput)
	if err != nil {
		rt.logger.Error(runCtx, "failed to execute agent workflow", slog.String("workflowID", workflowID), slog.Any("error", err))
		return nil, err
	}

	rt.logger.Debug(runCtx, "agent workflow (stream) executed", slog.String("workflowID", workflowID))

	streamStarted = true
	outCh := make(chan *types.AgentEvent, 64)
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
		var response *types.AgentResponse
		if err := workflowRun.Get(runCtx, &response); err != nil {
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
				if rt.temporalClient != nil {
					_ = rt.temporalClient.TerminateWorkflow(termCtx, workflowID, "", "run timeout")
				}
				termCancel()
				outCh <- &types.AgentEvent{
					Type:      types.AgentEventError,
					Content:   "request timed out (approval expired or deadline exceeded)",
					Timestamp: time.Now(),
				}
				return
			case wfErr := <-wfErrCh:
				rt.logger.Error(runCtx, "agent workflow failed (stream)", slog.String("workflowID", workflowID), slog.Any("error", wfErr))
				outCh <- &types.AgentEvent{
					Type:      types.AgentEventError,
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
				if streamCompleteEndsRun(ev, req.AgentName) {
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

// OnApproval completes a tool approval when using RunStream. Pass the string from ev.Approval
// (see the streaming examples) along with the chosen status.
func (rt *TemporalRuntime) OnApproval(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	if status != types.ApprovalStatusApproved && status != types.ApprovalStatusRejected {
		return fmt.Errorf("invalid approval status: %s", status)
	}
	taskToken, err := base64.StdEncoding.DecodeString(approvalToken)
	if err != nil {
		return fmt.Errorf("invalid approval token: %w", err)
	}
	return rt.temporalClient.CompleteActivity(ctx, taskToken, status, nil)
}

// ensureEventWorkflowStarted starts the event workflow once per agent. Sets activeEventWorkflowID on first call.
func (rt *TemporalRuntime) ensureEventWorkflowStarted(ctx context.Context, agentName string) (string, error) {
	rt.runMu.Lock()
	id := rt.activeEventWorkflowID
	rt.runMu.Unlock()
	if id != "" {
		rt.logger.Debug(ctx, "reusing existing event workflow", slog.String("eventWorkflowID", id))
		return id, nil
	}
	rt.runMu.Lock()
	defer rt.runMu.Unlock()
	if rt.activeEventWorkflowID != "" {
		return rt.activeEventWorkflowID, nil
	}

	eventWorkflowID := rt.getEventWorkflowID(agentName)
	rt.logger.Debug(ctx, "executing event workflow", slog.String("eventWorkflowID", eventWorkflowID))

	_, err := rt.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       eventWorkflowID,
		TaskQueue:                getEventTaskQueue(rt.taskQueue),
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, rt.AgentEventWorkflow)
	if err != nil {
		rt.logger.Error(ctx, "failed to start event workflow", slog.String("eventWorkflowID", eventWorkflowID), slog.Any("error", err))
		return "", err
	}
	rt.activeEventWorkflowID = eventWorkflowID
	rt.logger.Info(ctx, "event workflow started", slog.String("eventWorkflowID", eventWorkflowID))
	return eventWorkflowID, nil
}

// hasWorkers returns true if there are pollers on the given task queue.
// If taskQueue is empty, uses a.taskQueue.
// Polls DescribeTaskQueue for up to workersCheckTimeout (default 15s) before returning false.
func (rt *TemporalRuntime) hasWorkers(ctx context.Context, taskQueue string) bool {
	q := taskQueue
	if q == "" {
		q = rt.taskQueue
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
		res, err := rt.temporalClient.DescribeTaskQueue(ctx, q, enumspb.TASK_QUEUE_TYPE_WORKFLOW)
		if err == nil && len(res.GetPollers()) != 0 {
			rt.logger.Debug(ctx, "workers found on task queue", slog.String("taskQueue", q), slog.Int("pollers", len(res.GetPollers())))
			return true
		}
		if time.Now().After(deadlineTime) {
			rt.logger.Debug(ctx, "workers check timed out", slog.String("taskQueue", q))
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
func (rt *TemporalRuntime) createEventWorker() {
	rt.eventWorkerMu.Lock()
	defer rt.eventWorkerMu.Unlock()
	if rt.eventWorker != nil {
		rt.logger.Debug(context.Background(), "event worker already running", slog.String("taskQueue", rt.taskQueue))
		return
	}
	eventQueue := getEventTaskQueue(rt.taskQueue)
	rt.logger.Info(context.Background(), "starting event worker", slog.String("taskQueue", eventQueue))
	w := worker.New(rt.temporalClient, eventQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(rt.AgentEventWorkflow, workflow.RegisterOptions{Name: "AgentEventWorkflow"})
	w.RegisterActivityWithOptions(rt.EventPublishActivity, activity.RegisterOptions{Name: "EventPublishActivity"})
	rt.eventWorker = w
	go func() { _ = rt.eventWorker.Start() }()
}

// streamCompleteEndsRun is true when this complete event should end RunStream for the root agent.
// Sub-agent workflows emit AgentEventComplete too; those must not close the root stream.
// Empty AgentName counts as root for backward compatibility.
func streamCompleteEndsRun(ev *types.AgentEvent, rootName string) bool {
	if ev == nil || ev.Type != types.AgentEventComplete {
		return false
	}
	src := strings.TrimSpace(ev.AgentName)
	root := strings.TrimSpace(rootName)
	if src == "" {
		return true
	}
	return src == root
}

func getEventTaskQueue(taskQueue string) string {
	return taskQueue + "-events"
}

// beginRun marks the start of a run. Returns (cleanup, nil) on success; call cleanup (or defer) when the run ends.
// Returns (nil, ErrAgentAlreadyRunning) if a run is already active.
func (rt *TemporalRuntime) beginRun(workflowID string) (func(), error) {
	rt.runMu.Lock()
	defer rt.runMu.Unlock()
	if rt.activeRunWorkflowID != "" {
		rt.logger.Debug(context.Background(), "beginRun rejected: already running", slog.String("active", rt.activeRunWorkflowID), slog.String("requested", workflowID))
		return nil, ErrAgentAlreadyRunning
	}
	rt.activeRunWorkflowID = workflowID
	rt.logger.Debug(context.Background(), "beginRun", slog.String("workflowID", workflowID))
	return func() { rt.endRun() }, nil
}

// endRun clears the active run state when a run completes. Does not clear activeEventWorkflowID (per-agent).
func (rt *TemporalRuntime) endRun() {
	rt.runMu.Lock()
	defer rt.runMu.Unlock()
	if rt.activeRunWorkflowID != "" {
		rt.logger.Debug(context.Background(), "endRun", slog.String("workflowID", rt.activeRunWorkflowID))
	}
	rt.activeRunWorkflowID = ""
}

func (rt *TemporalRuntime) getWorkflowID(agentName string, isStream bool) string {
	id := uuid.New().String()
	if isStream {
		return fmt.Sprintf("agent-stream-%s-%s", agentName, id)
	}
	return fmt.Sprintf("agent-run-%s-%s", agentName, id)
}

func (rt *TemporalRuntime) getEventWorkflowID(agentName string) string {
	id := uuid.New().String()
	return fmt.Sprintf("agent-event-%s-%s", agentName, id)
}
