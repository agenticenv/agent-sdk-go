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

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/workflowstreams"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

var _ runtime.WorkerRuntime = (*TemporalRuntime)(nil)

const (
	// workersCheckTimeout is how long hasWorkers polls for pollers before giving up.
	workersCheckTimeout = 15 * time.Second

	// maxAgentNameWorkflowSegmentBytes caps the sanitized agent-name segment embedded in Temporal workflow IDs.
	// Shorter than typical server limits; truncation uses truncateUTF8String to avoid splitting UTF-8 code points.
	maxAgentNameWorkflowSegmentBytes = 128
)

// ErrAgentFingerprintMismatch is returned when the per-run agent fingerprint does not match the worker.
var ErrAgentFingerprintMismatch = errors.New("temporal: agent fingerprint mismatch (caller vs worker); redeploy worker or align config/registries or retry run")

// ToolsResolver resolves per-run tools from registries at activity entry (worker runtime).
type ToolsResolver func(ctx context.Context) ([]interfaces.Tool, error)

// TemporalRuntime implements [runtime.WorkerRuntime] using Temporal workflows and
// activities as the execution backend. Agent event delivery goes through the
// workflowstreams WorkflowStream (see stream.go), not an in-process event bus
// (LocalRuntime shares its bus onto nested local sub-agent runtimes instead).
// It embeds [base.Runtime] for the common agent fields (AgentSpec, AgentConfig, Tracer, Metrics,
// ToolExecutionMode) and holds all Temporal-specific connection and fingerprint state as flat fields.
type TemporalRuntime struct {
	base.Runtime // AgentSpec, AgentConfig, Tracer, Metrics, ToolExecutionMode

	// Temporal connection
	temporalConfig     *TemporalConfig
	temporalClient     client.Client
	taskQueue          string
	ownsTemporalClient bool
	// remoteWorker: true for NewAgentWorker (polls activities); false for client Agent runtime.
	remoteWorker bool

	logger logger.Logger

	// approvalHandler is the Run-path approval callback (agent WithApprovalHandler).
	// Nil when unset. Stream uses CUSTOM events + StreamHandle.Approve instead.
	approvalHandler types.ApprovalHandler

	// Fingerprint inputs captured at construction; per-run digest from [computeAgentFingerprintFromRuntime].
	policyFingerprint        string
	mcpFingerprint           string
	a2aFingerprint           string
	observabilityFingerprint string
	// agentMode is the string form of [types.AgentMode] (e.g. "interactive", "autonomous").
	agentMode            string
	retrieverFingerprint string
	hooksFingerprint     string

	// disableLocalWorker mirrors pkg/agent DisableLocalWorker: when false, the client embeds a worker
	// so Execute/ExecuteStream skip DescribeTaskQueue poller checks.
	disableLocalWorker bool
	// disableFingerprintCheck disables activity-time caller-vs-worker fingerprint verification.
	// Break-glass only: keep false in production for rollout/config safety.
	disableFingerprintCheck bool

	// resolveTools resolves tools from registries at activity time (worker runtime).
	resolveToolsFn ToolsResolver

	// activeRuns tracks in-flight Run handles by Temporal workflowID (agent-run-{name}-{runID}).
	// Thread-safe via store.KV. Used by Close and same-runtime GetRunHandle reuse.
	activeRuns *store.KV[string, *runHandle]
	// activeStreams tracks in-flight Stream handles by Temporal workflowID (agent-stream-{name}-{runID}).
	// Thread-safe via store.KV. Used by Close and same-runtime GetStreamHandle reuse.
	activeStreams *store.KV[string, *streamHandle]

	agentWorker   worker.Worker
	agentWorkerMu sync.Mutex
}

func NewTemporalRuntime(opts ...Option) (*TemporalRuntime, error) {
	rt, err := buildTemporalRuntime(opts...)
	if err != nil {
		return nil, err
	}
	rt.logger.Info(context.Background(), "runtime created",
		slog.String("scope", "runtime"),
		slog.String("name", rt.AgentSpec.Name),
		slog.String("taskQueue", rt.taskQueue))
	if rt.disableFingerprintCheck {
		rt.logger.Warn(context.Background(),
			"fingerprint verification is disabled (break-glass mode)",
			slog.String("scope", "runtime"),
			slog.String("name", rt.AgentSpec.Name),
			slog.String("taskQueue", rt.taskQueue))
	}
	return rt, nil
}

// TaskQueue returns the Temporal task queue this runtime was configured with.
func (rt *TemporalRuntime) TaskQueue() string {
	return rt.taskQueue
}

// fetchTools resolves tools from registries at activity time via [resolveToolsFn].
func (rt *TemporalRuntime) fetchTools(ctx context.Context) ([]interfaces.Tool, error) {
	if rt.resolveToolsFn == nil {
		return nil, fmt.Errorf("temporal: tools resolver is not configured")
	}
	return rt.resolveToolsFn(ctx)
}

// verifyAgentFingerprint compares caller vs worker config digest when fingerprint check is enabled.
// Pass nil tools to fetch via [fetchTools] internally; pass pre-fetched tools when the activity already resolved them.
func (rt *TemporalRuntime) verifyAgentFingerprint(ctx context.Context, callerFingerprint string, tools []interfaces.Tool) error {
	if rt.disableFingerprintCheck || callerFingerprint == "" {
		return nil
	}
	if tools == nil {
		var err error
		tools, err = rt.fetchTools(ctx)
		if err != nil {
			return err
		}
	}
	got := computeAgentFingerprintFromRuntime(rt, tools)
	if got != callerFingerprint {
		return fmt.Errorf("%w: worker=%q caller=%q", ErrAgentFingerprintMismatch, got, callerFingerprint)
	}
	return nil
}

// Start starts the worker (blocks until Stop is called).
func (rt *TemporalRuntime) Start(ctx context.Context) error {
	rt.logger.Info(ctx, "runtime worker starting", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))

	rt.agentWorkerMu.Lock()
	defer rt.agentWorkerMu.Unlock()
	if rt.agentWorker != nil {
		rt.logger.Debug(ctx, "runtime worker already running", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
		return nil
	}
	rt.logger.Debug(ctx, "runtime worker registering workflows and activities", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))

	workerOptions := worker.Options{}
	tracingInterceptor, err := newTemporalTracingInterceptor(rt.Tracer)
	if err != nil {
		rt.logger.Error(ctx, "failed to create tracing interceptor", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue), slog.Any("error", err))
		return err
	}
	if tracingInterceptor != nil {
		workerOptions.Interceptors = []interceptor.WorkerInterceptor{tracingInterceptor}
	}

	w := worker.New(rt.temporalClient, rt.taskQueue, workerOptions)
	w.RegisterWorkflowWithOptions(rt.AgentWorkflow, workflow.RegisterOptions{Name: "AgentWorkflow"})
	w.RegisterActivityWithOptions(rt.AgentLLMActivity, activity.RegisterOptions{Name: "AgentLLMActivity"})
	w.RegisterActivityWithOptions(rt.AgentLLMStreamActivity, activity.RegisterOptions{Name: "AgentLLMStreamActivity"})
	w.RegisterActivityWithOptions(rt.AgentRetrieverActivity, activity.RegisterOptions{Name: "AgentRetrieverActivity"})
	w.RegisterActivityWithOptions(rt.AgentMemoryRecallActivity, activity.RegisterOptions{Name: "AgentMemoryRecallActivity"})
	w.RegisterActivityWithOptions(rt.AgentMemoryStoreActivity, activity.RegisterOptions{Name: "AgentMemoryStoreActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolAuthorizeActivity, activity.RegisterOptions{Name: "AgentToolAuthorizeActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolApprovalActivity, activity.RegisterOptions{Name: "AgentToolApprovalActivity"})
	w.RegisterActivityWithOptions(rt.AgentBudgetApprovalActivity, activity.RegisterOptions{Name: "AgentBudgetApprovalActivity"})
	w.RegisterActivityWithOptions(rt.AgentToolExecuteActivity, activity.RegisterOptions{Name: "AgentToolExecuteActivity"})
	w.RegisterActivityWithOptions(rt.AgentWorkflowCleanupActivity, activity.RegisterOptions{Name: "AgentWorkflowCleanupActivity"})
	// PublishStreamEventActivity: used by sub-agent workflows to forward events to the root WorkflowStream.
	w.RegisterActivityWithOptions(rt.PublishStreamEventActivity, activity.RegisterOptions{Name: "PublishStreamEventActivity"})
	w.RegisterActivityWithOptions(rt.AddConversationMessagesActivity, activity.RegisterOptions{Name: "AddConversationMessagesActivity"})
	rt.agentWorker = w
	if startErr := rt.agentWorker.Start(); startErr != nil {
		rt.logger.Error(ctx, "failed to start runtime worker", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue), slog.Any("error", startErr))
		return startErr
	}
	rt.logger.Debug(ctx, "runtime worker started", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	return nil
}

// Stop stops the Temporal worker(s).
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

// cancelWorkflow cancels a workflow; if the Cancel RPC fails, terminates it.
// Note: a successful Cancel with no worker still leaves Cancel Requested — callers that
// must not strand the workflow should use [TemporalRuntime.stopWorkflow].
func (rt *TemporalRuntime) cancelWorkflow(ctx context.Context, workflowID, reason string) {
	if rt.temporalClient == nil || workflowID == "" {
		return
	}
	rt.logger.Debug(ctx, "runtime cancelling workflow",
		slog.String("scope", "runtime"),
		slog.String("workflowID", workflowID))
	if err := rt.temporalClient.CancelWorkflow(ctx, workflowID, ""); err != nil {
		rt.logger.Debug(ctx, "runtime cancel failed, terminating workflow",
			slog.String("scope", "runtime"),
			slog.String("workflowID", workflowID),
			slog.Any("error", err))
		_ = rt.temporalClient.TerminateWorkflow(ctx, workflowID, "", reason)
	}
}

// terminateWorkflow force-stops a workflow so awaitCompletion/Get/Events can finish
// even when no worker is available to process a Cancel.
func (rt *TemporalRuntime) terminateWorkflow(ctx context.Context, workflowID, reason string) {
	if rt.temporalClient == nil || workflowID == "" {
		return
	}
	rt.logger.Debug(ctx, "runtime terminating workflow",
		slog.String("scope", "runtime"),
		slog.String("workflowID", workflowID),
		slog.String("reason", reason))
	_ = rt.temporalClient.TerminateWorkflow(ctx, workflowID, "", reason)
}

// stopWorkflow stops a workflow after the run context ends, then waits for done.
//
//   - DeadlineExceeded (WithTimeout / Run|Stream ctx deadline): Terminate immediately so
//     subscribers unblock with a timeout error even when no worker is polling.
//   - Otherwise (explicit Cancel / parent cancel): Cancel first, then Terminate if the
//     workflow is still open after 3s (no-worker Cancel Requested hang).
//
// done is the handle Done channel from newRunHandle (always non-nil in production).
func (rt *TemporalRuntime) stopWorkflow(
	stopCtx context.Context,
	workflowID string,
	runErr error,
	done <-chan struct{},
) {
	if errors.Is(runErr, context.DeadlineExceeded) {
		rt.terminateWorkflow(stopCtx, workflowID, "run timeout")
		<-done
		return
	}

	rt.cancelWorkflow(stopCtx, workflowID, "run cancelled")
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
		rt.terminateWorkflow(stopCtx, workflowID, "run cancelled")
		select {
		case <-done:
		case <-stopCtx.Done():
		}
	}
}

// getRunResult waits for a workflow to finish and returns its AgentRunResult.
func (rt *TemporalRuntime) getRunResult(ctx context.Context, workflowID string) (*types.AgentRunResult, error) {
	if rt.temporalClient == nil {
		return nil, fmt.Errorf("temporal: getRunResult requires a Temporal client")
	}
	if workflowID == "" {
		return nil, fmt.Errorf("temporal: getRunResult requires workflowID")
	}
	run := rt.temporalClient.GetWorkflow(ctx, workflowID, "")
	var result *types.AgentRunResult
	err := run.Get(ctx, &result)
	return result, err
}

// getRunStatus describes a workflow and maps it to [types.RunStatus].
// Returns [types.ErrRunNotFound] when the workflow is unknown.
func (rt *TemporalRuntime) getRunStatus(ctx context.Context, workflowID string) (types.RunStatus, error) {
	if rt.temporalClient == nil {
		return "", fmt.Errorf("temporal: getRunStatus requires a Temporal client")
	}
	if workflowID == "" {
		return "", fmt.Errorf("temporal: getRunStatus requires workflowID")
	}
	desc, err := rt.temporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		if isNotFoundError(err) {
			return "", types.ErrRunNotFound
		}
		return "", err
	}
	return temporalStatusToRunStatus(desc.WorkflowExecutionInfo.GetStatus()), nil
}

func (rt *TemporalRuntime) Close() {
	rt.logger.Info(context.Background(), "runtime closing", slog.String("scope", "runtime"), slog.String("name", rt.AgentSpec.Name))

	ctx := context.Background()

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

// approve completes a tool approval request using the token from the approval event
// and the chosen status (e.g., [ApprovalStatusApproved] or [ApprovalStatusRejected]).
// Returns [ErrApprovalAlreadyResolved] when the token was already completed.
func (rt *TemporalRuntime) approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	if status != types.ApprovalStatusApproved && status != types.ApprovalStatusRejected {
		return fmt.Errorf("invalid approval status: %s", status)
	}
	taskToken, err := base64.StdEncoding.DecodeString(approvalToken)
	if err != nil {
		return fmt.Errorf("invalid approval token: %w", err)
	}
	if err := rt.temporalClient.CompleteActivity(ctx, taskToken, status, nil); err != nil {
		if isNotFoundError(err) {
			return types.ErrApprovalAlreadyResolved
		}
		return err
	}
	return nil
}

func agentNameFromRuntime(rt *TemporalRuntime) string {
	if rt == nil {
		return ""
	}
	return rt.AgentSpec.Name
}

// Run starts the agent workflow and returns a [runtime.RunHandle] immediately.
// Use [runtime.RunHandle.Get] or [runtime.RunHandle.Done] to wait for completion.
// When rt.approvalHandler is set, after ExecuteWorkflow succeeds a background goroutine
// subscribes to the WorkflowStream for CUSTOM approval events and completes them via CompleteActivity.
func (rt *TemporalRuntime) Run(ctx context.Context, req *runtime.RunRequest) (runtime.RunHandle, error) {
	if req == nil {
		return nil, fmt.Errorf("temporal: nil RunRequest")
	}

	rt.logger.Debug(ctx, "runtime run dispatch",
		slog.String("scope", "runtime"),
		slog.String("agent", agentNameFromRuntime(rt)),
		slog.Int("inputLen", len(req.UserPrompt)))

	runCtx, runCancel := context.WithCancel(ctx)
	if d := rt.AgentConfig.Limits.Timeout; d > 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var timeoutCancel context.CancelFunc
			runCtx, timeoutCancel = context.WithTimeout(runCtx, d)
			prev := runCancel
			runCancel = func() {
				timeoutCancel()
				prev()
			}
		}
	}

	conversationID := req.ConversationID
	memoryScope, memErr := rt.ResolveMemoryScope(runCtx)
	if memErr != nil {
		rt.logger.Warn(runCtx, "runtime memory scope resolve failed, continuing with empty scope",
			slog.String("scope", "runtime"),
			slog.Any("error", memErr))
		memoryScope = interfaces.MemoryScope{}
	}
	runID := uuid.New().String()

	threadID := conversationID
	if threadID == "" {
		threadID = strings.TrimSpace(rt.AgentSpec.Name)
		if threadID == "" {
			threadID = runID
		}
	}
	workflowID := rt.getWorkflowID(runID, agentNameFromRuntime(rt), false)

	rt.logger.Debug(runCtx, "runtime identifiers",
		slog.String("scope", "runtime"),
		slog.String("runID", runID),
		slog.String("threadID", threadID),
		slog.String("workflowID", workflowID))

	eventTypes := []events.AgentEventType{}
	if rt.approvalHandler != nil {
		eventTypes = []events.AgentEventType{events.AgentEventTypeCustom}
	}

	wfInput := AgentWorkflowInput{
		UserPrompt:       req.UserPrompt,
		RunID:            runID,
		StreamingEnabled: false,
		RootWorkflowID:   "",
		ConversationID:   conversationID,
		MemoryScope:      memoryScope,
		AgentFingerprint: computeAgentFingerprintFromRuntime(rt, req.Tools),
		EventTypes:       eventTypes,
		SubAgentDepth:    0,
		SubAgentRoutes:   buildSubAgentRoutes(req.SubAgents),
		MaxSubAgentDepth: req.MaxSubAgentDepth,
	}

	if !rt.skipHasWorkersPrecheck() {
		if !rt.hasWorkers(runCtx, rt.taskQueue) {
			runCancel()
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
		slog.Bool("hasApprovalHandler", rt.approvalHandler != nil))

	_, err := rt.temporalClient.ExecuteWorkflow(runCtx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: rt.taskQueue,
	}, rt.AgentWorkflow, wfInput)
	if err != nil {
		runCancel()
		rt.logger.Error(runCtx, "runtime workflow start failed",
			slog.String("scope", "runtime"),
			slog.String("workflowID", workflowID),
			slog.Any("error", err))
		return nil, err
	}

	var streamClient *workflowstreams.Client
	var subscribeCancel context.CancelFunc
	var approvalEventCh <-chan events.AgentEvent
	if rt.approvalHandler != nil {
		streamClient, approvalEventCh, subscribeCancel = rt.subscribeApprovalEvents(runCtx, workflowID)
	}

	handle := newRunHandle(runID, workflowID, rt, runCancel, func() {
		rt.activeRuns.Delete(workflowID)
	})
	rt.activeRuns.Set(workflowID, handle)
	go rt.driveRun(runCtx, runCancel, workflowID, handle, approvalEventCh, streamClient, subscribeCancel)
	return handle, nil
}

// subscribeApprovalEvents starts a WorkflowStream subscription for CUSTOM approval events
// after the workflow exists. Caller owns streamClient / subscribeCancel cleanup (via driveRun).
func (rt *TemporalRuntime) subscribeApprovalEvents(
	runCtx context.Context,
	workflowID string,
) (*workflowstreams.Client, <-chan events.AgentEvent, context.CancelFunc) {
	streamClient := newStreamClient(rt.temporalClient, workflowID)
	subscribeCtx, subscribeCancel := context.WithCancel(runCtx)

	approvalCh := make(chan events.AgentEvent, 16)
	go func() {
		defer close(approvalCh)
		for item, err := range streamClient.Subscribe(subscribeCtx, newStreamSubscribeOptions(0, []string{streamTopicEvents})) {
			if err != nil {
				return
			}
			ev, decErr := decodeStreamItem(item)
			if decErr != nil {
				rt.logger.Warn(subscribeCtx, "runtime approval event decode skipped",
					slog.String("scope", "runtime"), slog.Any("error", decErr))
				continue
			}
			if ev.Type() != events.AgentEventTypeCustom {
				continue
			}
			// Skip already-resolved approvals (e.g. GetRunHandle reconnect replaying from offset 0).
			if toolCallID, ok := approvalToolCallIDOf(ev); ok {
				if !rt.isPendingApproval(subscribeCtx, workflowID, toolCallID) {
					rt.logger.Debug(subscribeCtx, "runtime: skipping already-resolved approval",
						slog.String("scope", "runtime"),
						slog.String("workflowID", workflowID),
						slog.String("toolCallID", toolCallID))
					continue
				}
			}
			select {
			case approvalCh <- ev:
			case <-subscribeCtx.Done():
				return
			}
		}
	}()
	return streamClient, approvalCh, subscribeCancel
}

type approvalResponse struct {
	approvalToken string
	status        types.ApprovalStatus
}

// driveRun watches the run until [runHandle.Done], delivering approvals when configured.
// On runCtx cancellation (timeout/cancel), stops the workflow so Get/Events can finish
// (see [TemporalRuntime.stopWorkflow]).
func (rt *TemporalRuntime) driveRun(
	runCtx context.Context,
	runCancel context.CancelFunc,
	workflowID string,
	handle *runHandle,
	approvalEventCh <-chan events.AgentEvent,
	streamClient *workflowstreams.Client,
	subscribeCancel context.CancelFunc,
) {
	defer runCancel()
	defer func() {
		if subscribeCancel != nil {
			subscribeCancel()
		}
		if streamClient != nil {
			_ = streamClient.Close(context.Background())
		}
	}()

	approvalResponseCh := make(chan approvalResponse, 16)
	rt.logger.Debug(runCtx, "runtime watching run",
		slog.String("scope", "runtime"),
		slog.String("workflowID", workflowID))

	for {
		select {
		case <-handle.Done():
			res, err := handle.Get(context.Background())
			if err != nil {
				rt.logger.Error(runCtx, "runtime run completed with error",
					slog.String("scope", "runtime"),
					slog.String("runID", handle.ID()),
					slog.String("workflowID", workflowID),
					slog.Any("error", err))
			} else if res != nil {
				rt.logger.Debug(runCtx, "runtime run completed",
					slog.String("scope", "runtime"),
					slog.String("agentName", res.AgentName),
					slog.String("model", res.Model),
					slog.Int("contentLen", len(res.Content)))
			}
			return
		case <-runCtx.Done():
			runErr := runCtx.Err()
			rt.logger.Debug(runCtx, "runtime run cancelled",
				slog.String("scope", "runtime"),
				slog.String("workflowID", workflowID),
				slog.Any("error", runErr))
			handle.setStopCause(runErr)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
			rt.stopWorkflow(stopCtx, workflowID, runErr, handle.Done())
			stopCancel()
			return
		case ev, ok := <-approvalEventCh:
			if !ok {
				approvalEventCh = nil
				continue
			}
			if ev == nil || ev.Type() != events.AgentEventTypeCustom {
				continue
			}
			approvalEv, ok := ev.(*events.AgentCustomEvent)
			if !ok {
				continue
			}
			apprReq, token, err := prepareApprovalFromCustomEvent(approvalEv)
			if err != nil {
				if errors.Is(err, ErrNotApprovalCustomEvent) {
					continue
				}
				rt.logger.Error(runCtx, "runtime approval custom event decode failed",
					slog.String("scope", "runtime"), slog.Any("error", err))
				continue
			}
			apprReq.Respond = func(status types.ApprovalStatus) error {
				if status != types.ApprovalStatusRejected && status != types.ApprovalStatusApproved {
					return errors.New("invalid approval status")
				}
				approvalResponseCh <- approvalResponse{approvalToken: token, status: status}
				// TODO: Respond always returns nil today (async approve in driveRun). Later, surface
				// types.ErrApprovalAlreadyResolved (and other approve errors) to the handler so a
				// second UI can dismiss the prompt. Dual-process GetRunHandle is unsupported; document
				// that only the owner process should handle approvals.
				return nil
			}
			approvalCtx, cancel := context.WithTimeout(runCtx, rt.AgentConfig.Limits.ApprovalTimeout)
			rt.approvalHandler(approvalCtx, apprReq)
			cancel()
		case resp := <-approvalResponseCh:
			if err := rt.approve(runCtx, resp.approvalToken, resp.status); err != nil {
				if errors.Is(err, types.ErrApprovalAlreadyResolved) {
					rt.logger.Debug(runCtx, "runtime approval already resolved",
						slog.String("scope", "runtime"))
					continue
				}
				rt.logger.Error(runCtx, "runtime approval completion failed; approval activity will time out",
					slog.String("scope", "runtime"), slog.Any("error", err))
				continue
			}
		}
	}
}

// GetRunHandle reconnects to an existing non-stream agent run by runID.
//
// It derives the Temporal workflow ID (agent-run-…), describes the execution, and returns a
// [runtime.RunHandle]. Same-runtime: if the run is already in activeRuns, that handle is returned.
// Otherwise it re-attaches driveRun on an independent ctx (Background + Limits.Timeout) — the
// caller's ctx is only used for status/describe and does not CancelWorkflow when cancelled.
// Stop the run with [runtime.RunHandle.Cancel]. Approvals reattach when approvalHandler is set
// (QueryIsApprovalPending skips already-resolved CUSTOM events).
// Crash reconnect assumes the same agent config (see version/fingerprint TODO below).
//
// Returns [types.ErrRunNotFound] when runID is empty or the workflow is unknown.
// Returns [types.ErrRunAlreadyCompleted] when the workflow is already terminal.
func (rt *TemporalRuntime) GetRunHandle(ctx context.Context, runID string) (runtime.RunHandle, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, types.ErrRunNotFound
	}
	if rt.temporalClient == nil {
		return nil, fmt.Errorf("temporal: GetRunHandle requires a Temporal client")
	}

	workflowID := rt.getWorkflowID(runID, agentNameFromRuntime(rt), false)
	rt.logger.Debug(ctx, "runtime get run handle",
		slog.String("scope", "runtime"),
		slog.String("runID", runID),
		slog.String("workflowID", workflowID))

	status, err := rt.getRunStatus(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if status.IsTerminal() {
		return nil, types.ErrRunAlreadyCompleted
	}
	// TODO: On reconnect, compare the running workflow's agent version/fingerprint to this
	// runtime's agent. If they differ (redeploy/config change), reject GetRunHandle instead of
	// attaching driveRun/approvals. Crash reconnect assumes the same agent config; a version
	// mismatch should force a new run rather than a half-working reconnect.
	handle, ok := rt.activeRuns.Get(workflowID)
	if ok {
		return handle, nil
	}

	// Reattach driveRun with an independent run ctx. Do not use the caller's ctx here —
	// GetRunHandle/Get reconnect waiters often share a short deadline that must not
	// CancelWorkflow. Run(ctx) already bound the original run to the Run ctx.
	runCtx, runCancel := rt.newDriveContext(context.Background())

	var streamClient *workflowstreams.Client
	var subscribeCancel context.CancelFunc
	var approvalEventCh <-chan events.AgentEvent
	if rt.approvalHandler != nil {
		streamClient, approvalEventCh, subscribeCancel = rt.subscribeApprovalEvents(runCtx, workflowID)
	}

	handle = newRunHandle(runID, workflowID, rt, runCancel, func() {
		rt.activeRuns.Delete(workflowID)
	})
	rt.activeRuns.Set(workflowID, handle)
	go rt.driveRun(runCtx, runCancel, workflowID, handle, approvalEventCh, streamClient, subscribeCancel)
	return handle, nil
}

// OnApproval is a deprecated Runtime-interface wrapper around [TemporalRuntime.approve].
// Prefer [runtime.StreamHandle.Approve]. Removed in v0.4.0.
func (rt *TemporalRuntime) OnApproval(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	return rt.approve(ctx, approvalToken, status)
}

// Stream starts the agent Temporal workflow and returns a [runtime.StreamHandle] immediately.
//
// It does not open the event subscription here — call [runtime.StreamHandle.Events] (optionally
// with fromOffset > 0 to reconnect). The handle owns Status/Cancel/Events for this runID.
// Same-runtime reuse goes through activeStreams (see [TemporalRuntime.GetStreamHandle]).
//
// Cancelling ctx cancels the agent run (same idea as [TemporalRuntime.Run]). Cancelling the
// context passed to [runtime.StreamHandle.Events] only stops that subscriber — it does not
// cancel the Temporal workflow. Use a separate Events ctx for reconnect / "subscriber gone".
// Agent Limits.Timeout still applies when the Stream ctx has no deadline.
//
// Identifiers: runID is minted here; workflowID uses the stream naming convention
// (agent-stream-…); threadID prefers ConversationID for synthetic RUN_STARTED/FINISHED.
// Registers in activeStreams; cleanup deletes the entry from awaitCompletion when the
// workflow finishes. Cancel/timeout stop the workflow via driveStream.
func (rt *TemporalRuntime) Stream(ctx context.Context, req *runtime.RunRequest) (runtime.StreamHandle, error) {
	if req == nil {
		return nil, fmt.Errorf("temporal: nil RunRequest")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rt.logger.Debug(ctx, "runtime stream run dispatch",
		slog.String("scope", "runtime"),
		slog.String("agent", agentNameFromRuntime(rt)),
		slog.Int("inputLen", len(req.UserPrompt)))

	runCtx, runCancel := rt.newDriveContext(ctx)

	conversationID := req.ConversationID
	memoryScope, memErr := rt.ResolveMemoryScope(ctx)
	if memErr != nil {
		rt.logger.Warn(ctx, "runtime memory scope resolve failed, continuing with empty scope",
			slog.String("scope", "runtime"),
			slog.Any("error", memErr))
		memoryScope = interfaces.MemoryScope{}
	}

	runID := uuid.New().String()
	// threadID labels synthetic lifecycle events; fall back to agent name then runID.
	threadID := conversationID
	if threadID == "" {
		threadID = strings.TrimSpace(rt.AgentSpec.Name)
		if threadID == "" {
			threadID = runID
		}
	}
	// isStream=true → durable stream workflow ID (distinct from Run's non-stream ID).
	workflowID := rt.getWorkflowID(runID, agentNameFromRuntime(rt), true)

	rt.logger.Debug(ctx, "runtime identifiers",
		slog.String("scope", "runtime"),
		slog.String("runID", runID),
		slog.String("threadID", threadID),
		slog.String("workflowID", workflowID))

	streamEventTypes := []events.AgentEventType{events.AgentEventAll}
	if len(req.EventTypes) > 0 {
		streamEventTypes = req.EventTypes
	}

	wfInput := AgentWorkflowInput{
		UserPrompt:       req.UserPrompt,
		RunID:            runID,
		RootWorkflowID:   "",
		StreamingEnabled: req.EnableLLMStream,
		ConversationID:   conversationID,
		MemoryScope:      memoryScope,
		AgentFingerprint: computeAgentFingerprintFromRuntime(rt, req.Tools),
		EventTypes:       streamEventTypes,
		SubAgentDepth:    0,
		SubAgentRoutes:   buildSubAgentRoutes(req.SubAgents),
		MaxSubAgentDepth: req.MaxSubAgentDepth,
	}

	// Fail fast when no pollers are on the task queue (skipped for embedded local workers).
	if !rt.skipHasWorkersPrecheck() {
		if !rt.hasWorkers(ctx, rt.taskQueue) {
			runCancel()
			rt.logger.Warn(ctx, "no workers on task queue", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
			return nil, fmt.Errorf("no workers available on task queue %s", rt.taskQueue)
		}
		rt.logger.Debug(ctx, "task queue has workers (stream)", slog.String("scope", "runtime"), slog.String("taskQueue", rt.taskQueue))
	} else {
		rt.logger.Debug(ctx, "skipping task queue poller check",
			slog.String("scope", "runtime"),
			slog.String("taskQueue", rt.taskQueue),
			slog.String("reason", rt.hasWorkersPrecheckSkipReason()))
	}

	rt.logger.Debug(ctx, "runtime workflow execute (stream)",
		slog.String("scope", "runtime"),
		slog.String("workflowID", workflowID))

	_, err := rt.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: rt.taskQueue,
	}, rt.AgentWorkflow, wfInput)
	if err != nil {
		runCancel()
		rt.logger.Error(ctx, "runtime workflow start failed",
			slog.String("scope", "runtime"),
			slog.String("workflowID", workflowID),
			slog.Any("error", err))
		return nil, err
	}

	rt.logger.Debug(ctx, "runtime workflow started (stream)",
		slog.String("scope", "runtime"),
		slog.String("workflowID", workflowID))

	handle := newStreamHandle(runID, workflowID, threadID, rt, runCancel, func() {
		rt.activeStreams.Delete(workflowID)
	})
	rt.activeStreams.Set(workflowID, handle)
	go rt.driveStream(runCtx, runCancel, workflowID, handle)
	return handle, nil
}

// GetStreamHandle reconnects to an existing stream agent run by runID.
//
// It derives the Temporal stream workflow ID (agent-stream-…), describes the execution, and
// returns a [runtime.StreamHandle]. Same-runtime: if the stream is already in activeStreams,
// that handle is returned. Otherwise it registers a new handle (cleanup on awaitCompletion)
// and starts driveStream on an independent ctx (Background + Limits.Timeout) — the caller's
// ctx is only used for status/describe and does not CancelWorkflow when cancelled. Stop the
// run with [runtime.StreamHandle.Cancel]. Call [runtime.StreamHandle.Events] (optionally with
// fromOffset > 0) to subscribe.
// Crash reconnect assumes the same agent config (see version/fingerprint TODO below).
//
// Returns [types.ErrStreamNotFound] when runID is empty or the workflow is unknown.
// Returns [types.ErrRunAlreadyCompleted] when the workflow is already terminal.
func (rt *TemporalRuntime) GetStreamHandle(ctx context.Context, runID string) (runtime.StreamHandle, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, types.ErrStreamNotFound
	}
	if rt.temporalClient == nil {
		return nil, fmt.Errorf("temporal: GetStreamHandle requires a Temporal client")
	}

	workflowID := rt.getWorkflowID(runID, agentNameFromRuntime(rt), true)
	rt.logger.Debug(ctx, "runtime get stream handle",
		slog.String("scope", "runtime"),
		slog.String("runID", runID),
		slog.String("workflowID", workflowID))

	desc, err := rt.temporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		if isNotFoundError(err) {
			return nil, types.ErrStreamNotFound
		}
		return nil, err
	}
	st := temporalStatusToRunStatus(desc.WorkflowExecutionInfo.GetStatus())
	if st.IsTerminal() {
		return nil, types.ErrRunAlreadyCompleted
	}

	// TODO: On reconnect, compare the running workflow's agent version/fingerprint to this
	// runtime's agent. If they differ (redeploy/config change), reject GetStreamHandle.
	// Crash reconnect assumes the same agent config; a version mismatch should force a new
	// stream rather than a half-working reconnect.
	handle, ok := rt.activeStreams.Get(workflowID)
	if ok {
		return handle, nil
	}

	// Reattach driveStream with an independent run ctx. Do not use the caller's ctx here —
	// GetStreamHandle/Events reconnect waiters often share a short deadline that must not
	// CancelWorkflow. Stream(ctx) already bound the original run to the Stream ctx.
	runCtx, runCancel := rt.newDriveContext(context.Background())

	// threadID for synthetic lifecycle events; ConversationID is not available on reconnect.
	threadID := strings.TrimSpace(rt.AgentSpec.Name)
	if threadID == "" {
		threadID = runID
	}

	handle = newStreamHandle(runID, workflowID, threadID, rt, runCancel, func() {
		rt.activeStreams.Delete(workflowID)
	})
	rt.activeStreams.Set(workflowID, handle)
	go rt.driveStream(runCtx, runCancel, workflowID, handle)
	return handle, nil
}

// newDriveContext returns the driveRun / driveStream context.
// parent is typically the Run()/Stream() caller ctx (cancel parent → cancel run), or
// context.Background() for GetRunHandle / GetStreamHandle reattach. When parent has no
// deadline and Limits.Timeout is set, that timeout is applied.
func (rt *TemporalRuntime) newDriveContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	runCtx, runCancel := context.WithCancel(parent)
	if d := rt.AgentConfig.Limits.Timeout; d > 0 {
		if _, hasDeadline := parent.Deadline(); !hasDeadline {
			var timeoutCancel context.CancelFunc
			runCtx, timeoutCancel = context.WithTimeout(runCtx, d)
			prev := runCancel
			runCancel = func() {
				timeoutCancel()
				prev()
			}
		}
	}
	return runCtx, runCancel
}

// driveStream watches the stream until [streamHandle.Done].
// On runCtx cancellation (timeout/cancel), stops the workflow so await/Events can finish
// (see [TemporalRuntime.stopWorkflow]).
// No approval subscription — Stream uses Events + Approve instead of driveRun's path.
func (rt *TemporalRuntime) driveStream(
	runCtx context.Context,
	runCancel context.CancelFunc,
	workflowID string,
	handle *streamHandle,
) {
	defer runCancel()
	select {
	case <-handle.Done():
		return
	case <-runCtx.Done():
		runErr := runCtx.Err()
		rt.logger.Debug(runCtx, "runtime stream cancelled",
			slog.String("scope", "runtime"),
			slog.String("workflowID", workflowID),
			slog.Any("error", runErr))
		handle.setStopCause(runErr)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		rt.stopWorkflow(stopCtx, workflowID, runErr, handle.Done())
		stopCancel()
	}
}

// temporalStatusToRunStatus maps a Temporal workflow execution status enum to [types.RunStatus].
func temporalStatusToRunStatus(s enumspb.WorkflowExecutionStatus) types.RunStatus {
	switch s {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
		return types.StatusRunning
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return types.StatusCompleted
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		return types.StatusFailed
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return types.StatusCancelled
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return types.StatusFailed // treat timeout as failure
	case enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return types.StatusCancelled // treat termination as cancellation
	default:
		return types.StatusPending
	}
}

// isPendingApproval queries the workflow to check whether toolCallID still has a pending approval.
// Returns true when the query fails (fail-open: forward the event rather than silently drop it).
func (rt *TemporalRuntime) isPendingApproval(ctx context.Context, workflowID string, toolCallID string) bool {
	resp, err := rt.temporalClient.QueryWorkflow(ctx, workflowID, "", QueryIsApprovalPending, toolCallID)
	if err != nil {
		// Query failed (workflow closed, network issue, etc.) — treat as pending to avoid dropping
		// a legitimately open approval request.
		rt.logger.Warn(ctx, "stream: pending-approval query failed, forwarding approval event",
			slog.String("scope", "runtime"),
			slog.String("workflowID", workflowID),
			slog.String("toolCallID", toolCallID),
			slog.Any("error", err))
		return true
	}
	var pending bool
	if err := resp.Get(&pending); err != nil {
		rt.logger.Warn(ctx, "stream: pending-approval query decode failed, forwarding approval event",
			slog.String("scope", "runtime"),
			slog.String("workflowID", workflowID),
			slog.String("toolCallID", toolCallID),
			slog.Any("error", err))
		return true
	}
	return pending
}

// isNotFoundError reports whether err is a Temporal "not found" service error, which is returned
// by CompleteActivity when the task token refers to an activity that no longer exists (already
// completed, timed out, or the workflow closed).
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// Temporal SDK wraps gRPC NOT_FOUND as serviceerror.NotFoundError. Check the message as a
	// fallback since the concrete type lives in an internal package.
	msg := err.Error()
	return strings.Contains(msg, "NotFound") || strings.Contains(msg, "not found") || strings.Contains(msg, "activity task not found")
}

// skipHasWorkersPrecheck is true when Execute/ExecuteStream should not poll DescribeTaskQueue for pollers
// before starting the workflow.
func (rt *TemporalRuntime) skipHasWorkersPrecheck() bool {
	if rt.agentMode == string(types.AgentModeAutonomous) {
		return true
	}
	if !rt.disableLocalWorker {
		return true
	}
	return false
}

func (rt *TemporalRuntime) hasWorkersPrecheckSkipReason() string {
	if rt.agentMode == string(types.AgentModeAutonomous) {
		return "autonomous_mode"
	}
	if !rt.disableLocalWorker {
		return "embedded_local_worker"
	}
	return ""
}

// hasWorkers returns true if there are pollers on the given task queue.
// Polls DescribeTaskQueue for up to workersCheckTimeout before returning false.
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
		}
	}
}

func (rt *TemporalRuntime) getWorkflowID(runID, agentName string, isStream bool) string {
	name := sanitizeTemporalWorkflowIDSegment(agentName)
	if isStream {
		return fmt.Sprintf("agent-stream-%s-%s", name, runID)
	}
	return fmt.Sprintf("agent-run-%s-%s", name, runID)
}

// sanitizeTemporalWorkflowIDSegment maps a human-readable label to a safe workflow ID segment:
// alphanumeric, hyphen, underscore, dot; spaces and other runes become hyphens.
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
