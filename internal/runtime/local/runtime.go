package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	durable "github.com/agenticenv/durable-go"
	"github.com/google/uuid"
)

var _ sdkruntime.Runtime = (*LocalRuntime)(nil)

// LocalRuntime executes the agent loop in-process, embedding base.Runtime for shared
// core methods and holding local-specific fields (logger, eventbus).
type LocalRuntime struct {
	base.Runtime

	logger   logger.Logger
	eventbus eventbus.EventBus
	// ownsEventBus is true when this runtime created the bus (NewLocalRuntime).
	// setEventBus clears it so a shared parent bus is not torn down by this runtime later.
	ownsEventBus bool

	// approvalHandler is the Run-path approval callback (agent WithApprovalHandler).
	// Nil when unset. Stream uses CUSTOM events + Approve instead.
	approvalHandler types.ApprovalHandler

	// pendingApprovals holds token → resolve channel for tools awaiting human approval.
	// Used by approve() to unblock executeSingleTool when the caller responds via
	// StreamHandle.Approve (streaming path). Thread-safe: parallel tool calls each register
	// their own token. Only used on the non-durable path — see pendingApprovals doc on
	// approve() for how durable mode branches to durable.CompleteStep instead.
	pendingApprovals sync.Map // key: string token, value: chan types.ApprovalStatus

	// --- durable-go wiring (see durable_engine.go). Zero values (engine nil) behave
	// exactly like the pre-durability runtime: Run/Stream/GetRunHandle/GetStreamHandle
	// take the original in-memory-only paths below unchanged. ---

	// localConfig is the durability configuration from [WithLocalConfig]. Nil means the
	// zero-value LocalConfig (durable by default with all-default knobs).
	localConfig *LocalConfig
	// engine is the durable-go engine backing this runtime, or nil when durability is
	// disabled ([LocalConfig.Durability] false).
	engine *durable.Engine
	// ownsEngine is true when this runtime constructed engine (LocalConfig.Engine was not
	// set) and must Close it; false for a caller-supplied Engine.
	ownsEngine bool
	// taskID is the durable-go taskID this runtime registers itself under (sanitized
	// agent name). Only meaningful when engine != nil.
	taskID string
	// toolsResolver resolves the static (registry/MCP/A2A/sub-agent-tool-schema) tool list
	// for a run that has no live per-request Tools available — used to rehydrate a resumed
	// durable run after a process restart. May be nil (resumed runs then see no tools).
	toolsResolver ToolsResolver
	// liveRuns holds the non-JSON-serializable per-run extras (Tools, SubAgentRoutes,
	// ChannelName, EventTypes) for runs started in this process via Run/Stream, keyed by
	// runID, so the durable-go Task.Exec closure (registered once, long before any
	// particular run's Tools are known) can look them up when it actually executes.
	// Entries are added just before durable.RunTask and removed when Task.Exec returns.
	liveRuns sync.Map // key: string runID, value: *liveRunExtras
}

// NewLocalRuntime constructs a LocalRuntime from functional options.
func NewLocalRuntime(opts ...Option) (*LocalRuntime, error) {
	r, err := buildLocalRuntime(opts...)
	if err != nil {
		return nil, err
	}
	if err := r.setupDurability(); err != nil {
		return nil, err
	}
	r.logger.Info(context.Background(), "runtime created",
		slog.String("scope", "runtime"),
		slog.String("name", r.AgentSpec.Name),
		slog.Bool("durable", r.engine != nil))
	r.eventbus = eventbus.NewInmem(r.logger)
	r.ownsEventBus = true
	return r, nil
}

// localChannelName returns the eventbus channel name for one run.
func localChannelName(runID string) string {
	return "agent-event-" + runID
}

// subscribeToAgentEvents subscribes to the run channel and returns a typed event channel
// plus a close function. Events are decoded from the raw JSON published by publishEventToChannel.
func (rt *LocalRuntime) subscribeToAgentEvents(ctx context.Context, channel string) (<-chan events.AgentEvent, func() error, error) {
	rawCh, closeFn, err := rt.eventbus.Subscribe(ctx, channel)
	if err != nil {
		return nil, nil, fmt.Errorf("local: subscribe to channel %q: %w", channel, err)
	}
	outCh := make(chan events.AgentEvent, 64)
	go func() {
		defer close(outCh)
		for data := range rawCh {
			ev, err := events.EventFromJSON(data)
			if err != nil {
				rt.logger.Warn(ctx, "local: failed to decode agent event",
					slog.String("scope", "runtime"),
					slog.Any("error", err))
				continue
			}
			if ev != nil {
				outCh <- ev
			}
		}
	}()
	return outCh, closeFn, nil
}

// publishLifecycleEvent publishes a lifecycle event (RUN_STARTED, RUN_FINISHED, RUN_ERROR) to the
// run channel. Uses context.Background so a cancelled runCtx never drops the terminal event.
func (rt *LocalRuntime) publishLifecycleEvent(channel string, ev events.AgentEvent) {
	if rt.eventbus == nil || channel == "" || ev == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := rt.eventbus.Publish(context.Background(), channel, data); err != nil {
		rt.logger.Warn(context.Background(), "local: lifecycle event publish failed",
			slog.String("scope", "runtime"),
			slog.String("channel", channel),
			slog.String("type", string(ev.Type())),
			slog.Any("error", err))
	}
}

// Run starts the agent loop in a background goroutine and returns a [sdkruntime.RunHandle]
// immediately. Approval is handled inline via rt.approvalHandler (no out-of-band tokens).
// Use [sdkruntime.RunHandle.Get] or [sdkruntime.RunHandle.Done] to wait for completion.
func (rt *LocalRuntime) Run(ctx context.Context, req *sdkruntime.RunRequest) (sdkruntime.RunHandle, error) {
	if req == nil {
		return nil, fmt.Errorf("local: nil RunRequest")
	}

	rt.logger.Debug(ctx, "runtime run",
		slog.String("scope", "runtime"),
		slog.String("agent", rt.AgentSpec.Name),
		slog.Int("inputLen", len(req.UserPrompt)))

	runID := uuid.New().String()
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

	rt.shareEventBusWithSubAgents(req.SubAgents)

	handle := newRunHandle(runID, rt, runCancel)
	if rt.engine != nil {
		handle.setDurable(rt.engine, rt.taskID)
		dto := rt.buildDurableRunInput(ctx, req, false)
		rt.storeLiveRunExtras(runID, req.Tools, req.SubAgents, "")
		go rt.driveDurableRun(runCtx, dto, handle)
		return handle, nil
	}
	go rt.driveRun(runCtx, req, handle)
	return handle, nil
}

// buildDurableRunInput resolves the memory scope (using the caller's ctx — durable-go's
// own execution ctx is disconnected from it, see durableExec) and assembles the resume
// DTO for a fresh durable Run/Stream call. isStream selects the CUSTOM-events-only
// (Run) vs. all-events-by-default (Stream) EventTypes default, matching driveRun/driveStream.
func (rt *LocalRuntime) buildDurableRunInput(ctx context.Context, req *sdkruntime.RunRequest, isStream bool) durableRunInput {
	memoryScope, memErr := rt.ResolveMemoryScope(ctx)
	if memErr != nil {
		rt.logger.Warn(ctx, "runtime memory scope resolve failed, continuing with empty scope",
			slog.String("scope", "runtime"),
			slog.Any("error", memErr))
		memoryScope = interfaces.MemoryScope{}
	}

	var eventTypes []events.AgentEventType
	if isStream {
		eventTypes = []events.AgentEventType{events.AgentEventAll}
		if len(req.EventTypes) > 0 {
			eventTypes = req.EventTypes
		}
	} else if rt.approvalHandler != nil {
		eventTypes = []events.AgentEventType{events.AgentEventTypeCustom}
	}

	return durableRunInput{
		UserPrompt:       req.UserPrompt,
		ConversationID:   req.ConversationID,
		IsStream:         isStream,
		StreamingEnabled: req.EnableLLMStream,
		EventTypes:       eventTypes,
		MaxSubAgentDepth: req.MaxSubAgentDepth,
		MemoryScope:      memoryScope,
		Budget:           rt.AgentConfig.Limits.Budget,
	}
}

// driveRun drives the agent loop for a [runHandle] and signals completion via [runHandle.markDone].
// Must be called in a goroutine started by [LocalRuntime.Run].
func (rt *LocalRuntime) driveRun(runCtx context.Context, req *sdkruntime.RunRequest, handle *runHandle) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("local: panic in agent loop: %v", r)
			rt.logger.Error(runCtx, "runtime run panicked",
				slog.String("scope", "runtime"),
				slog.String("runID", handle.id),
				slog.Any("error", err))
			handle.markDone(nil, err)
		}
	}()

	memoryScope, memErr := rt.ResolveMemoryScope(runCtx)
	if memErr != nil {
		rt.logger.Warn(runCtx, "runtime memory scope resolve failed, continuing with empty scope",
			slog.String("scope", "runtime"),
			slog.Any("error", memErr))
		memoryScope = interfaces.MemoryScope{}
	}

	// EventTypes: empty by default; CUSTOM only when an approval handler is set.
	eventTypes := []events.AgentEventType{}
	if rt.approvalHandler != nil {
		eventTypes = []events.AgentEventType{events.AgentEventTypeCustom}
	}

	budgetTracker := base.NewBudgetTracker(rt.AgentConfig.Limits.Budget)
	loopResult, err := rt.executeAgentLoop(runCtx, AgentLoopInput{
		UserPrompt:       req.UserPrompt,
		RunID:            handle.id,
		ConversationID:   req.ConversationID,
		MemoryScope:      memoryScope,
		StreamingEnabled: false,
		ChannelName:      "",
		EventTypes:       eventTypes,
		ApprovalHandler:  rt.approvalHandler,
		SubAgentRoutes:   buildSubAgentRoutes(req.SubAgents),
		SubAgentDepth:    0,
		MaxSubAgentDepth: req.MaxSubAgentDepth,
		Tools:            req.Tools,
		BudgetTracker:    budgetTracker,
		EnforceBudget:    budgetTracker != nil,
	})
	if err != nil {
		rt.logger.Error(runCtx, "runtime run failed",
			slog.String("scope", "runtime"),
			slog.String("runID", handle.id),
			slog.Any("error", err))
		handle.markDone(nil, err)
		return
	}

	handle.markDone(&types.AgentRunResult{
		Content:   loopResult.Content,
		AgentName: strings.TrimSpace(rt.AgentSpec.Name),
		Model:     rt.AgentConfig.LLM.Client.GetModel(),
		Metadata:  map[string]any{},
		RunID:     handle.id,
		LLMUsage:  loopResult.LLMUsage,
		Telemetry: loopResult.Telemetry,
	}, nil)
}

// GetRunHandle reconnects to runID.
//
// Non-durable: always returns [types.ErrRunNotFound] — LocalRuntime tracks nothing
// durably; same-process live handles are managed by the agent run registry, and after a
// process crash there is nothing to reconnect to.
//
// Durable ([LocalConfig.Durability] true, the default): looks up runID's persisted
// TaskInfo. A terminal run (Completed/Failed, including a cancelled run — see
// durable.ErrRunCancelled) returns [types.ErrRunAlreadyCompleted]; an unknown runID
// returns [types.ErrRunNotFound]. Otherwise re-drives it via durable.RunTask, which
// resumes from wherever it left off — fast-replaying already-completed steps (no LLM/tool
// re-calls) and continuing live from the first new step. Tools are rehydrated via
// [ToolsResolver] when set (nil otherwise — see durableExec); sub-agent delegation
// routes are not recoverable across a process restart on this interface (no request
// payload survives it) — a delegation tool call on a resumed run gets the existing
// "Sub-agent delegation not available for this runtime" fallback message.
func (rt *LocalRuntime) GetRunHandle(ctx context.Context, runID string) (sdkruntime.RunHandle, error) {
	if rt.engine == nil {
		return nil, types.ErrRunNotFound
	}
	// Empty runID must never reach durable.RunTask (via driveDurableRun below): durable-go
	// treats "" as "resume the oldest Running/Waiting run for this taskID" — the wrong run
	// entirely when more than one is in flight. Reject it here instead.
	if strings.TrimSpace(runID) == "" {
		return nil, types.ErrRunNotFound
	}
	info, ok, err := rt.engine.GetTask(ctx, rt.taskID, runID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, types.ErrRunNotFound
	}
	if info.Status == durable.StatusCompleted || info.Status == durable.StatusFailed {
		return nil, types.ErrRunAlreadyCompleted
	}

	runCtx, runCancel := context.WithCancel(ctx)
	handle := newRunHandle(runID, rt, runCancel)
	handle.setDurable(rt.engine, rt.taskID)
	go rt.driveDurableRun(runCtx, durableRunInput{}, handle)
	return handle, nil
}

// OnApproval is a deprecated Runtime-interface wrapper around [LocalRuntime.approve].
// Prefer [sdkruntime.StreamHandle.Approve]. Removed in v0.4.0.
func (rt *LocalRuntime) OnApproval(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	return rt.approve(ctx, approvalToken, status)
}

// Stream starts the agent loop in a background goroutine and returns a [sdkruntime.StreamHandle]
// immediately. Subscribe via [sdkruntime.StreamHandle.Events] (offset 0 only on LocalRuntime).
// RUN_STARTED is emitted before the loop begins; RUN_FINISHED or RUN_ERROR closes the channel.
//
// Cancelling ctx cancels the agent run. The context passed to [sdkruntime.StreamHandle.Events]
// is independent on Temporal; on LocalRuntime Events ignores that ctx (channel already open).
// Agent Limits.Timeout applies when ctx has no deadline.
func (rt *LocalRuntime) Stream(ctx context.Context, req *sdkruntime.RunRequest) (sdkruntime.StreamHandle, error) {
	if req == nil {
		return nil, fmt.Errorf("local: nil RunRequest")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rt.logger.Debug(ctx, "runtime stream",
		slog.String("scope", "runtime"),
		slog.String("agent", rt.AgentSpec.Name),
		slog.Int("inputLen", len(req.UserPrompt)))

	rt.shareEventBusWithSubAgents(req.SubAgents)

	runID := uuid.New().String()
	threadID := req.ConversationID
	if threadID == "" {
		threadID = runID
	}
	channel := localChannelName(runID)

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

	// Subscribe before starting the loop so no events are lost.
	eventCh, closeSub, err := rt.subscribeToAgentEvents(runCtx, channel)
	if err != nil {
		runCancel()
		return nil, err
	}

	handle := newStreamHandle(runID, rt, runCancel, eventCh)
	if rt.engine != nil {
		handle.setDurable(rt.engine, rt.taskID)
		dto := rt.buildDurableRunInput(ctx, req, true)
		rt.storeLiveRunExtras(runID, req.Tools, req.SubAgents, channel)
		go func() {
			defer func() { _ = closeSub() }()
			rt.driveDurableStream(runCtx, dto, handle, channel, threadID, true)
		}()
		return handle, nil
	}
	rt.publishLifecycleEvent(channel, events.NewAgentRunStartedEvent(threadID, runID))
	go rt.driveStream(runCtx, req, handle, channel, threadID, closeSub)
	return handle, nil
}

// driveStream drives the streaming agent loop for a [streamHandle], publishes the
// terminal lifecycle event, and signals completion via [runHandle.markDone].
// Must be called in a goroutine started by [LocalRuntime.Stream].
func (rt *LocalRuntime) driveStream(
	runCtx context.Context,
	req *sdkruntime.RunRequest,
	handle *streamHandle,
	channel string,
	threadID string,
	closeSub func() error,
) {
	defer func() { _ = closeSub() }()
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("local: panic in agent loop: %v", r)
			rt.logger.Error(runCtx, "runtime stream run panicked",
				slog.String("scope", "runtime"),
				slog.String("runID", handle.id),
				slog.Any("error", err))
			rt.publishLifecycleEvent(channel, events.NewAgentRunErrorEvent(err.Error()))
			handle.markDone(nil, err)
		}
	}()

	memoryScope, memErr := rt.ResolveMemoryScope(runCtx)
	if memErr != nil {
		rt.logger.Warn(runCtx, "runtime memory scope resolve failed, continuing with empty scope",
			slog.String("scope", "runtime"),
			slog.Any("error", memErr))
		memoryScope = interfaces.MemoryScope{}
	}

	streamEventTypes := []events.AgentEventType{events.AgentEventAll}
	if len(req.EventTypes) > 0 {
		streamEventTypes = req.EventTypes
	}

	streamBudgetTracker := base.NewBudgetTracker(rt.AgentConfig.Limits.Budget)
	result, loopErr := rt.executeAgentLoop(runCtx, AgentLoopInput{
		UserPrompt:       req.UserPrompt,
		RunID:            handle.id,
		ConversationID:   req.ConversationID,
		MemoryScope:      memoryScope,
		StreamingEnabled: req.EnableLLMStream,
		ChannelName:      channel,
		EventTypes:       streamEventTypes,
		ApprovalHandler:  rt.approvalHandler,
		SubAgentRoutes:   buildSubAgentRoutes(req.SubAgents),
		SubAgentDepth:    0,
		MaxSubAgentDepth: req.MaxSubAgentDepth,
		Tools:            req.Tools,
		BudgetTracker:    streamBudgetTracker,
		EnforceBudget:    streamBudgetTracker != nil,
	})
	if loopErr != nil {
		rt.logger.Error(runCtx, "runtime stream run failed",
			slog.String("scope", "runtime"),
			slog.String("runID", handle.id),
			slog.Any("error", loopErr))
		rt.publishLifecycleEvent(channel, events.NewAgentRunErrorEvent(loopErr.Error()))
		handle.markDone(nil, loopErr)
		return
	}

	agentRunResult := &types.AgentRunResult{
		Content:   result.Content,
		AgentName: strings.TrimSpace(rt.AgentSpec.Name),
		Model:     rt.AgentConfig.LLM.Client.GetModel(),
		Metadata:  map[string]any{},
		RunID:     handle.id,
		LLMUsage:  result.LLMUsage,
		Telemetry: result.Telemetry,
	}
	rt.publishLifecycleEvent(channel, events.NewAgentRunFinishedEvent(threadID, handle.id, agentRunResult))
	handle.markDone(agentRunResult, nil)
}

// GetStreamHandle reconnects to runID's stream.
//
// Non-durable: always returns [types.ErrStreamNotFound] — LocalRuntime tracks nothing
// durably; same-process live handles are managed by the agent stream registry, and after
// a process crash there is nothing to reconnect to.
//
// Durable ([LocalConfig.Durability] true, the default): looks up runID's persisted
// TaskInfo the same way [LocalRuntime.GetRunHandle] does ([types.ErrStreamNotFound] /
// [types.ErrRunAlreadyCompleted] in place of the Run-path sentinels). On success,
// subscribes a fresh channel, replays already-completed steps as one coarse
// [events.AgentCustomEventNameStepReplayed] event each (see replayStepHistory — step
// granularity only, never the original token-by-token stream; document this to callers),
// then re-drives the run via durable.RunTask so anything not yet done continues live on
// that same channel. See [LocalRuntime.GetRunHandle] for the tools/sub-agent-routes
// rehydration caveat, which applies identically here.
func (rt *LocalRuntime) GetStreamHandle(ctx context.Context, runID string) (sdkruntime.StreamHandle, error) {
	if rt.engine == nil {
		return nil, types.ErrStreamNotFound
	}
	// See the identical guard in GetRunHandle: an empty runID must never reach
	// durable.RunTask (via driveDurableStream below).
	if strings.TrimSpace(runID) == "" {
		return nil, types.ErrStreamNotFound
	}
	info, ok, err := rt.engine.GetTask(ctx, rt.taskID, runID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, types.ErrStreamNotFound
	}
	if info.Status == durable.StatusCompleted || info.Status == durable.StatusFailed {
		return nil, types.ErrRunAlreadyCompleted
	}

	threadID := runID
	channel := localChannelName(runID)
	runCtx, runCancel := context.WithCancel(ctx)
	eventCh, closeSub, err := rt.subscribeToAgentEvents(runCtx, channel)
	if err != nil {
		runCancel()
		return nil, err
	}
	rt.replayStepHistory(runCtx, runID, channel)

	handle := newStreamHandle(runID, rt, runCancel, eventCh)
	handle.setDurable(rt.engine, rt.taskID)
	go func() {
		defer func() { _ = closeSub() }()
		rt.driveDurableStream(runCtx, durableRunInput{}, handle, channel, threadID, false)
	}()
	return handle, nil
}

// approve resolves a pending tool, sub-agent, or budget approval identified by
// approvalToken with status.
//
// Durable runtimes (engine != nil) always use durable-go tokens: approvalToken is a
// [durable.StepToken] and resolution goes through [durable.CompleteStep], which
// delivers status directly as that step's result (see durableApprovalWait) — this
// applies uniformly to both the Run-path ApprovalHandler's Respond callback and
// StreamHandle.Approve, since both durable.RunTask closures are cooperating on the same
// underlying step. Returns [types.ErrApprovalAlreadyResolved] when the step is already
// completed (durable.ErrRunAlreadyFinished) or the token cannot be decoded.
//
// Non-durable runtimes keep the original in-memory channel lookup: executeSingleTool
// registers a token and blocks; the caller receives a CUSTOM event on the stream with
// that token and calls [sdkruntime.StreamHandle.Approve] to unblock. Returns
// [types.ErrApprovalAlreadyResolved] when the token is unknown or was already resolved
// (same sentinel as Temporal when CompleteActivity reports not found).
func (rt *LocalRuntime) approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	if rt.engine != nil {
		if err := durable.CompleteStep(ctx, rt.engine, approvalToken, status); err != nil {
			if errors.Is(err, durable.ErrRunAlreadyFinished) || errors.Is(err, durable.ErrInvalidToken) {
				return types.ErrApprovalAlreadyResolved
			}
			return err
		}
		return nil
	}
	val, ok := rt.pendingApprovals.LoadAndDelete(approvalToken)
	if !ok {
		return types.ErrApprovalAlreadyResolved
	}
	ch := val.(chan types.ApprovalStatus)
	ch <- status
	return nil
}

// Close releases runtime resources. When this runtime owns the event bus
// ([ownsEventBus]), the bus is closed; shared buses from [setEventBus] are left alone.
// When this runtime constructed its durable-go engine ([ownsEngine]), the engine is
// closed too — that cancels every in-flight run on it, not just this agent's. A
// caller-supplied [LocalConfig.Engine] is never closed here; the caller owns it.
func (rt *LocalRuntime) Close() {
	if rt.ownsEventBus && rt.eventbus != nil {
		rt.eventbus.Close()
		rt.ownsEventBus = false
	}
	if rt.ownsEngine && rt.engine != nil {
		if err := rt.engine.Close(); err != nil {
			rt.logger.Warn(context.Background(), "local: durable engine close failed",
				slog.String("scope", "runtime"),
				slog.Any("error", err))
		}
		rt.ownsEngine = false
	}
	rt.logger.Info(context.Background(), "runtime closed",
		slog.String("scope", "runtime"),
		slog.String("name", rt.AgentSpec.Name))
}

// setEventBus replaces the runtime's event bus (parent sharing onto a sub-agent).
// Clears ownsEventBus so Close does not tear down a bus owned by another runtime.
func (rt *LocalRuntime) setEventBus(bus eventbus.EventBus) {
	rt.eventbus = bus
	rt.ownsEventBus = false
}

// shareEventBusWithSubAgents sets this runtime's event bus on each nested LocalRuntime
// in the SubAgentSpec tree. Called from Run and Stream with req.SubAgents.
func (rt *LocalRuntime) shareEventBusWithSubAgents(subAgents []*sdkruntime.SubAgentSpec) {
	for _, sub := range subAgents {
		if sub != nil {
			shareEventBusWithSubAgent(rt.eventbus, sub)
		}
	}
}

// shareEventBusWithSubAgent sets bus on sub.Runtime when it is a *LocalRuntime,
// then recurses into Children.
func shareEventBusWithSubAgent(bus eventbus.EventBus, sub *sdkruntime.SubAgentSpec) {
	if sub == nil || bus == nil {
		return
	}
	if lr, ok := sub.Runtime.(*LocalRuntime); ok {
		lr.setEventBus(bus)
	}
	for _, child := range sub.Children {
		shareEventBusWithSubAgent(bus, child)
	}
}
