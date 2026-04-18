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
	"unicode/utf8"

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

var (
	_ runtime.WorkerRuntime   = (*TemporalRuntime)(nil)
	_ runtime.EventBusRuntime = (*TemporalRuntime)(nil) // embeds [runtime.Runtime]
)

const (
	// workersCheckTimeout is how long hasWorkers polls for pollers before giving up.
	workersCheckTimeout = 15 * time.Second

	// maxAgentNameWorkflowSegmentBytes caps the sanitized agent-name segment embedded in Temporal workflow IDs.
	// Shorter than typical server limits; truncation uses truncateUTF8String to avoid splitting UTF-8 code points.
	maxAgentNameWorkflowSegmentBytes = 128
)

// ErrAgentAlreadyRunning is returned when Execute, ExecuteStream, or RunAsync is called while a run is already in progress.
var ErrAgentAlreadyRunning = errors.New("agent already has an active run")

// ErrAgentFingerprintMismatch is returned when workflow input fingerprint does not match the worker.
var ErrAgentFingerprintMismatch = errors.New("temporal: agent fingerprint mismatch (caller vs worker); redeploy worker or align agent config")

type TemporalRuntime struct {
	TemporalRuntimeConfig

	// agentFingerprint is ComputeAgentFingerprint(BuildAgentFingerprintPayload(...)) at NewTemporalRuntime; immutable for this runtime.
	agentFingerprint string

	eventbus              eventbus.EventBus
	runMu                 sync.Mutex
	activeRunWorkflowID   string
	activeEventWorkflowID string
	// eventWorkflowIDOnce + suffix make agent-event-<name>-<uuid> unique per TemporalRuntime so
	// multiple agents in one process do not collide; lazy start still uses the same ID for that runtime.
	eventWorkflowIDOnce   sync.Once
	eventWorkflowIDSuffix string

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
	cfg.logger.Info(context.Background(), "runtime created", slog.String("scope", "runtime"), slog.String("name", cfg.AgentSpec.Name), slog.String("taskQueue", cfg.taskQueue))
	fp := computeAgentFingerprintFromRuntimeConfig(cfg)
	return &TemporalRuntime{
		TemporalRuntimeConfig: *cfg,
		agentFingerprint:      fp,
		eventbus:              eventbus.NewInmem(cfg.logger),
	}, nil
}

// verifyAgentFingerprint returns an error when want does not equal the runtime's agent fingerprint
// (computed at [NewTemporalRuntime]).
func (rt *TemporalRuntime) verifyAgentFingerprint(want string) error {
	if rt.agentFingerprint != want {
		return fmt.Errorf("%w: worker=%q caller=%q", ErrAgentFingerprintMismatch, rt.agentFingerprint, want)
	}
	return nil
}

// SetEventBus replaces the in-process event bus. Sub-agent runtimes are wired to the parent
// agent's bus so delegation and approval events fan in correctly.
func (rt *TemporalRuntime) SetEventBus(eventbus eventbus.EventBus) {
	rt.eventbus = eventbus
}

// GetEventBus returns the event bus for the runtime.
func (rt *TemporalRuntime) GetEventBus() eventbus.EventBus {
	return rt.eventbus
}

// Start starts the worker (blocks until Stop is called).
func (rt *TemporalRuntime) Start(ctx context.Context) error {
	rt.logger.Info(ctx, "runtime worker starting", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	// createAgentWorker creates and registers a Temporal worker for the agent's run workflow and activities.

	rt.agentWorkerMu.Lock()
	defer rt.agentWorkerMu.Unlock()
	if rt.agentWorker != nil {
		rt.logger.Debug(ctx, "runtime worker already running", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
		return nil
	}
	rt.logger.Debug(ctx, "runtime worker registering workflows and activities", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	w := worker.New(rt.temporalClient, rt.taskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(rt.AgentWorkflow, workflow.RegisterOptions{Name: "AgentWorkflow"})
	w.RegisterActivityWithOptions(rt.AgentLLMActivity, activity.RegisterOptions{Name: "AgentLLMActivity"})
	w.RegisterActivityWithOptions(rt.AgentLLMStreamActivity, activity.RegisterOptions{Name: "AgentLLMStreamActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolAuthorizeActivity, activity.RegisterOptions{Name: "AgentToolAuthorizeActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolApprovalActivity, activity.RegisterOptions{Name: "AgentToolApprovalActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolExecuteActivity, activity.RegisterOptions{Name: "AgentToolExecuteActivity"})
	w.RegisterActivityWithOptions(rt.SendAgentEventUpdateActivity, activity.RegisterOptions{Name: "SendAgentEventUpdateActivity"})
	w.RegisterActivityWithOptions(rt.AddConversationMessagesActivity, activity.RegisterOptions{Name: "AddConversationMessagesActivity"})
	rt.agentWorker = w
	err := rt.agentWorker.Start()
	if err != nil {
		rt.logger.Error(ctx, "failed to start runtime worker", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue), slog.Any("error", err))
		return err
	}
	rt.logger.Debug(ctx, "runtime worker started", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	return nil
}

// Stop stops the Temporal worker(s). Called when the agent package stops an embedded worker or closes the runtime.
func (rt *TemporalRuntime) Stop() {
	ctx := context.Background()
	if rt.remoteWorker {
		rt.logger.Debug(ctx, "runtime stopping remote worker path", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
		if rt.agentWorker != nil {
			rt.logger.Debug(ctx, "runtime stopping remote worker", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
			rt.agentWorker.Stop()
		}
		if rt.temporalClient != nil && rt.ownsTemporalClient {
			rt.logger.Debug(ctx, "runtime closing owned client (remote worker)", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
			rt.temporalClient.Close()
		}
		rt.logger.Debug(ctx, "runtime remote worker stopped", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	} else {
		rt.logger.Debug(ctx, "runtime stop skipped (local worker embed)", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	}
}

func (rt *TemporalRuntime) Close() {
	rt.logger.Info(context.Background(), "runtime closing", slog.String("scope", "runtime"), slog.String("name", rt.AgentSpec.Name))

	rt.runMu.Lock()
	workflowID := rt.activeRunWorkflowID
	eventWorkflowID := rt.activeEventWorkflowID
	rt.runMu.Unlock()

	ctx := context.Background()

	if rt.temporalClient != nil {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if workflowID != "" {
			rt.logger.Debug(ctx, "runtime terminating active run", slog.String("scope", "runtime"), slog.String("workflowID", workflowID))
			_ = rt.temporalClient.TerminateWorkflow(ctx, workflowID, "", "agent closed")
		}
		if eventWorkflowID != "" {
			rt.logger.Debug(ctx, "runtime signaling event pipeline to complete", slog.String("scope", "runtime"), slog.String("eventWorkflowID", eventWorkflowID))
			_ = rt.temporalClient.SignalWorkflow(ctx, eventWorkflowID, "", eventWorkflowCompleteSignal, nil)
			// Wait for event workflow to complete gracefully (worker must stay running to process the signal)
			run := rt.temporalClient.GetWorkflow(ctx, eventWorkflowID, "")
			_ = run.Get(ctx, nil)
		}
	}

	if rt.eventWorker != nil {
		rt.logger.Debug(ctx, "runtime stopping event worker", slog.String("scope", "runtime"))
		rt.eventWorker.Stop()
	}

	if rt.agentWorker != nil {
		rt.logger.Debug(ctx, "runtime stopping task worker", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
		rt.agentWorker.Stop()
	}

	if rt.temporalClient != nil && rt.ownsTemporalClient {
		rt.logger.Debug(ctx, "runtime closing owned temporal client", slog.String("scope", "runtime"))
		rt.temporalClient.Close()
	}
	rt.logger.Info(ctx, "runtime closed", slog.String("scope", "runtime"), slog.String("name", rt.AgentSpec.Name))
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

func agentNameFromExecuteRequest(req *runtime.ExecuteRequest) string {
	if req == nil || req.AgentSpec == nil {
		return ""
	}
	return req.AgentSpec.Name
}

func (rt *TemporalRuntime) Execute(ctx context.Context, req *runtime.ExecuteRequest) (*types.AgentResponse, error) {
	rt.logger.Debug(ctx, "runtime run dispatch", slog.String("scope", "runtime"), slog.String("agent", agentNameFromExecuteRequest(req)), slog.Int("inputLen", len(req.UserPrompt)))

	runCtx := ctx
	d := rt.AgentExecution.Limits.Timeout
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	workflowID := rt.getWorkflowID(agentNameFromExecuteRequest(req), false)

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
		AgentFingerprint: rt.agentFingerprint,
		EventTypes:       []types.AgentEventType{},
		SubAgentDepth:    0,
		SubAgentRoutes:   req.SubAgentRoutes,
		MaxSubAgentDepth: req.MaxSubAgentDepth,
	}

	if rt.enableRemoteWorkers {
		rt.createEventWorker()
		var err error
		wfInput.EventWorkflowID, wfInput.EventTaskQueue, err = rt.resolveEventPipeline(runCtx, agentNameFromExecuteRequest(req))
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
			rt.logger.Error(runCtx, "runtime event subscribe failed", slog.String("scope", "runtime"), slog.String("workflowID", workflowID), slog.Any("error", err))
			return nil, err
		}
		defer func() { _ = closeEvent() }()
	}

	if !rt.skipHasWorkersPrecheck() {
		hasWorkers := rt.hasWorkers(runCtx, rt.taskQueue)
		if !hasWorkers {
			rt.logger.Warn(runCtx, "no workers on task queue", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
			return nil, fmt.Errorf("no workers available on task queue %s", rt.taskQueue)
		}
		rt.logger.Debug(runCtx, "task queue has workers", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	} else {
		rt.logger.Debug(runCtx, "skipping task queue poller check", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue), slog.String("reason", rt.hasWorkersPrecheckSkipReason()))
	}

	rt.logger.Debug(runCtx, "runtime workflow execute",
		slog.String("scope", "runtime"),
		slog.String("workflowID", workflowID),
		slog.Bool("streamingEnabled", wfInput.StreamingEnabled),
		slog.Bool("hasEventPipeline", wfInput.EventWorkflowID != ""))

	workfowRun, err := rt.temporalClient.ExecuteWorkflow(runCtx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: rt.taskQueue,
	}, rt.AgentWorkflow, wfInput)
	if err != nil {
		rt.logger.Error(runCtx, "runtime workflow start failed", slog.String("scope", "runtime"), slog.String("workflowID", workflowID), slog.Any("error", err))
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

	rt.logger.Debug(runCtx, "runtime waiting for run result", slog.String("scope", "runtime"), slog.String("workflowID", workflowID))

	for {
		select {
		case r := <-resultCh:
			rt.logger.Debug(runCtx, "runtime run completed",
				slog.String("scope", "runtime"),
				slog.String("agentName", r.AgentName),
				slog.String("model", r.Model),
				slog.Int("contentLen", len(r.Content)))
			return r, nil
		case err := <-errCh:
			rt.logger.Error(runCtx, "runtime run failed", slog.String("scope", "runtime"), slog.String("workflowID", workflowID), slog.Any("error", err))
			return nil, err
		case <-runCtx.Done():
			rt.logger.Debug(runCtx, "runtime run cancelled", slog.String("scope", "runtime"), slog.String("workflowID", workflowID), slog.Any("error", runCtx.Err()))
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
			approvalCtx, cancel := context.WithTimeout(runCtx, rt.AgentExecution.Limits.ApprovalTimeout)
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
				rt.logger.Error(runCtx, "runtime approval completion failed", slog.String("scope", "runtime"), slog.Any("error", err))
				return nil, err
			}
		}
	}
}

func (rt *TemporalRuntime) ExecuteStream(ctx context.Context, req *runtime.ExecuteRequest) (chan *types.AgentEvent, error) {
	rt.logger.Debug(ctx, "runtime stream run dispatch", slog.String("scope", "runtime"), slog.String("agent", agentNameFromExecuteRequest(req)), slog.Int("inputLen", len(req.UserPrompt)))

	workflowID := rt.getWorkflowID(agentNameFromExecuteRequest(req), true)
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

	var eventWorkflowID, eventTaskQueue string
	if rt.enableRemoteWorkers {
		rt.createEventWorker()
		var err error
		eventWorkflowID, eventTaskQueue, err = rt.resolveEventPipeline(ctx, agentNameFromExecuteRequest(req))
		if err != nil {
			return nil, err
		}
	}

	streamEventTypes := []types.AgentEventType{types.AgentEventAll}
	if len(req.EventTypes) > 0 {
		streamEventTypes = req.EventTypes
	}
	wfInput := AgentWorkflowInput{
		UserPrompt:       req.UserPrompt,
		EventWorkflowID:  eventWorkflowID,
		EventTaskQueue:   eventTaskQueue,
		LocalChannelName: eventChannelName(workflowID),
		StreamingEnabled: req.StreamingEnabled,
		ConversationID:   req.ConversationID,
		AgentFingerprint: rt.agentFingerprint,
		EventTypes:       streamEventTypes,
		SubAgentDepth:    0,
		SubAgentRoutes:   req.SubAgentRoutes,
		MaxSubAgentDepth: req.MaxSubAgentDepth,
	}

	runCtx := ctx
	var runCancel context.CancelFunc
	d := rt.AgentExecution.Limits.Timeout
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
		rt.logger.Error(runCtx, "runtime event subscribe failed", slog.String("scope", "runtime"), slog.String("channel", wfInput.LocalChannelName), slog.Any("error", err))
		return nil, err
	}
	rt.logger.Debug(runCtx, "runtime subscribed to event stream", slog.String("scope", "runtime"), slog.String("channel", wfInput.LocalChannelName))
	defer func() {
		if !streamStarted && closeEvent != nil {
			_ = closeEvent()
		}
	}()

	if !rt.skipHasWorkersPrecheck() {
		hasWorkers := rt.hasWorkers(ctx, rt.taskQueue)
		if !hasWorkers {
			rt.logger.Warn(runCtx, "no workers on task queue", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
			return nil, fmt.Errorf("no workers available on task queue %s", rt.taskQueue)
		}
		rt.logger.Debug(runCtx, "task queue has workers (stream)", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	} else {
		rt.logger.Debug(runCtx, "skipping task queue poller check", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue), slog.String("reason", rt.hasWorkersPrecheckSkipReason()))
	}

	rt.logger.Debug(runCtx, "runtime workflow execute (stream)", slog.String("scope", "runtime"), slog.String("workflowID", workflowID))

	workflowRun, err := rt.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: rt.taskQueue,
	}, rt.AgentWorkflow, wfInput)
	if err != nil {
		rt.logger.Error(runCtx, "runtime workflow start failed", slog.String("scope", "runtime"), slog.String("workflowID", workflowID), slog.Any("error", err))
		return nil, err
	}

	rt.logger.Debug(runCtx, "runtime workflow started (stream)", slog.String("scope", "runtime"), slog.String("workflowID", workflowID))

	streamStarted = true
	outCh := make(chan *types.AgentEvent, 64)
	wfErrCh := make(chan error, 1)
	// Buffered so workflowRun.Get can signal completion without blocking before getWG.Done (avoids
	// deadlock with the forwarding goroutine blocked on getWG.Wait after a streamed root complete).
	workflowDoneCh := make(chan *types.AgentResponse, 1)
	var getWG sync.WaitGroup
	getWG.Add(1)
	go func() {
		defer getWG.Done()
		// cleanup/endRun only after Get returns. runCtx must stay valid until then so the workflow
		// can finish (after root complete, work remains: e.g. SendAgentEventUpdateActivity—longer
		// when a sub-agent child workflow just ran).
		defer cleanup()
		var response *types.AgentResponse
		if err := workflowRun.Get(runCtx, &response); err != nil {
			// Cancel the run timeout context on failure so the event loop and subscriber unwind.
			if runCancel != nil {
				runCancel()
			}
			wfErrCh <- err
			return
		}
		// On success, do not cancel runCtx here. Get can return before AgentEventComplete is
		// delivered (async pipeline: UpdateWorkflow → event workflow → publish activity → inmem).
		// Cancelling immediately races with the forwarding goroutine and produces a spurious
		// "request timed out" AgentEventError. The loop cancels after it forwards root complete.
		// Non-blocking: if the buffer is full the forward goroutine already consumed the signal.
		select {
		case workflowDoneCh <- response:
		default:
		}
	}()
	go func() {
		defer close(outCh)
		defer func() { _ = closeEvent() }()
		rootName := agentNameFromExecuteRequest(req)
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
				rt.logger.Error(runCtx, "runtime stream run failed", slog.String("scope", "runtime"), slog.String("workflowID", workflowID), slog.Any("error", wfErr))
				outCh <- &types.AgentEvent{
					Type:      types.AgentEventError,
					Content:   wfErr.Error(),
					Timestamp: time.Now(),
				}
				return
			case resp := <-workflowDoneCh:
				// workflowRun.Get returned: publish complete from the durable result immediately.
				// Do not wait on further eventCh reads (avoids hanging; streaming tail may be skipped).
				outCh <- syntheticStreamCompleteEvent(resp, rootName)
				if runCancel != nil {
					runCancel()
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
				if streamCompleteEndsRun(ev, rootName) {
					// Root complete is emitted before the workflow returns; after sub-agent delegation
					// there is often more server-side work. Wait for Get+cleanup so the run reaches
					// Completed before we close the channel (avoids Close() terminating a live run).
					getWG.Wait()
					if runCancel != nil {
						runCancel()
					}
					return
				}
			}
		}
	}()

	return outCh, nil
}

// OnApproval completes a tool approval when using ExecuteStream. Pass the string from ev.Approval
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

// resolveEventPipeline returns the deterministic event workflow ID and event task queue when remote
// workers are enabled. The AgentEventWorkflow is started lazily on the first UpdateWithStart from an activity.
func (rt *TemporalRuntime) resolveEventPipeline(ctx context.Context, agentName string) (eventWorkflowID string, eventTaskQueue string, err error) {
	eventWorkflowID = rt.getEventWorkflowID(agentName)
	eventTaskQueue = getEventTaskQueue(rt.taskQueue)
	rt.runMu.Lock()
	if rt.activeEventWorkflowID == "" {
		rt.activeEventWorkflowID = eventWorkflowID
		rt.logger.Info(ctx, "runtime event pipeline (lazy start on first update)",
			slog.String("scope", "runtime"),
			slog.String("eventWorkflowID", eventWorkflowID),
			slog.String("eventTaskQueue", eventTaskQueue))
	}
	rt.runMu.Unlock()
	return eventWorkflowID, eventTaskQueue, nil
}

// skipHasWorkersPrecheck is true when Execute/ExecuteStream should not poll DescribeTaskQueue for pollers
// before starting the workflow. Only these paths call Execute/ExecuteStream (client [Agent]; remoteWorker is always false).
// Skip when mode is autonomous, or when an embedded worker polls in-process ([DisableLocalWorker] false).
func (rt *TemporalRuntime) skipHasWorkersPrecheck() bool {
	if rt.AgentMode == string(types.AgentModeAutonomous) {
		return true
	}
	if !rt.DisableLocalWorker {
		return true
	}
	return false
}

func (rt *TemporalRuntime) hasWorkersPrecheckSkipReason() string {
	if rt.AgentMode == string(types.AgentModeAutonomous) {
		return "autonomous_mode"
	}
	if !rt.DisableLocalWorker {
		return "embedded_local_worker"
	}
	return ""
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
			rt.logger.Debug(ctx, "task queue pollers seen", slog.String("scope", "runtime"), slog.String("taskQueue", q), slog.Int("pollers", len(res.GetPollers())))
			return true
		}
		if time.Now().After(deadlineTime) {
			rt.logger.Debug(ctx, "task queue worker wait timed out", slog.String("scope", "runtime"), slog.String("taskQueue", q))
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

// createEventWorker starts the event worker if not already running. Called when ExecuteStream or
// approval handling is needed. Per-agent mutex allows parallel creation across different agents
// while preventing double-creation when Execute and ExecuteStream are invoked concurrently on the same agent.
func (rt *TemporalRuntime) createEventWorker() {
	rt.eventWorkerMu.Lock()
	defer rt.eventWorkerMu.Unlock()
	if rt.eventWorker != nil {
		rt.logger.Debug(context.Background(), "runtime event worker already running", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
		return
	}
	eventQueue := getEventTaskQueue(rt.taskQueue)
	rt.logger.Info(context.Background(), "runtime event worker starting", slog.String("scope", "runtime"), slog.String("taskQueue", eventQueue))
	w := worker.New(rt.temporalClient, eventQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(rt.AgentEventWorkflow, workflow.RegisterOptions{Name: "AgentEventWorkflow"})
	w.RegisterActivityWithOptions(rt.EventPublishActivity, activity.RegisterOptions{Name: "EventPublishActivity"})
	rt.eventWorker = w
	go func() { _ = rt.eventWorker.Start() }()
}

// syntheticStreamCompleteEvent builds a root AgentEventComplete from workflow.Get. ExecuteStream
// sends this as soon as Get returns so the client is not blocked on the async event pipeline.
func syntheticStreamCompleteEvent(resp *types.AgentResponse, rootName string) *types.AgentEvent {
	ev := &types.AgentEvent{
		Type:      types.AgentEventComplete,
		Timestamp: time.Now(),
	}
	if resp != nil {
		ev.Content = resp.Content
		ev.Usage = resp.Usage
		if strings.TrimSpace(resp.AgentName) != "" {
			ev.AgentName = strings.TrimSpace(resp.AgentName)
		} else if strings.TrimSpace(rootName) != "" {
			ev.AgentName = strings.TrimSpace(rootName)
		}
	} else if strings.TrimSpace(rootName) != "" {
		ev.AgentName = strings.TrimSpace(rootName)
	}
	return ev
}

// streamCompleteEndsRun is true when this complete event should end ExecuteStream for the root agent.
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
		rt.logger.Debug(context.Background(), "runtime run rejected: already active", slog.String("scope", "runtime"), slog.String("active", rt.activeRunWorkflowID), slog.String("requested", workflowID))
		return nil, ErrAgentAlreadyRunning
	}
	rt.activeRunWorkflowID = workflowID
	rt.logger.Debug(context.Background(), "runtime run started", slog.String("scope", "runtime"), slog.String("workflowID", workflowID))
	return func() { rt.endRun() }, nil
}

// endRun clears the active run state when a run completes. Does not clear activeEventWorkflowID (per-agent).
func (rt *TemporalRuntime) endRun() {
	rt.runMu.Lock()
	defer rt.runMu.Unlock()
	if rt.activeRunWorkflowID != "" {
		rt.logger.Debug(context.Background(), "runtime run finished", slog.String("scope", "runtime"), slog.String("workflowID", rt.activeRunWorkflowID))
	}
	rt.activeRunWorkflowID = ""
}

func (rt *TemporalRuntime) getWorkflowID(agentName string, isStream bool) string {
	id := uuid.New().String()
	name := sanitizeTemporalWorkflowIDSegment(agentName)
	if isStream {
		return fmt.Sprintf("agent-stream-%s-%s", name, id)
	}
	return fmt.Sprintf("agent-run-%s-%s", name, id)
}

func (rt *TemporalRuntime) getEventWorkflowID(agentName string) string {
	rt.eventWorkflowIDOnce.Do(func() {
		rt.eventWorkflowIDSuffix = uuid.New().String()
	})
	return fmt.Sprintf("agent-event-%s-%s", sanitizeTemporalWorkflowIDSegment(agentName), rt.eventWorkflowIDSuffix)
}

// sanitizeTemporalWorkflowIDSegment maps a human-readable label (e.g. [runtime.AgentSpec] Name) to a safe
// workflow ID segment: alphanumeric, hyphen, underscore, dot; spaces and other runes become hyphens.
// The result is capped at [maxAgentNameWorkflowSegmentBytes] using UTF-8-safe truncation.
func sanitizeTemporalWorkflowIDSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "agent"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t':
			b.WriteByte('-')
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return truncateUTF8String(out, maxAgentNameWorkflowSegmentBytes)
}

// truncateUTF8String returns s if len(s) <= maxBytes; otherwise returns a prefix of at most maxBytes bytes
// that is valid UTF-8 (does not split a multibyte code point).
func truncateUTF8String(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
