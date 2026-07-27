package local

import (
	"context"
	"encoding/json"
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
	// their own token.
	pendingApprovals sync.Map // key: string token, value: chan types.ApprovalStatus
}

// NewLocalRuntime constructs a LocalRuntime from functional options.
func NewLocalRuntime(opts ...Option) (*LocalRuntime, error) {
	r, err := buildLocalRuntime(opts...)
	if err != nil {
		return nil, err
	}
	r.logger.Info(context.Background(), "runtime created",
		slog.String("scope", "runtime"),
		slog.String("name", r.AgentSpec.Name))
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
	go rt.driveRun(runCtx, req, handle)
	return handle, nil
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

	loopResult, err := rt.RunAgentLoop(runCtx, AgentLoopInput{
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

// GetRunHandle always returns [types.ErrRunNotFound]. LocalRuntime does not
// track runs durably; same-process live handles are managed by the agent run registry.
// After a process crash there is nothing to reconnect to.
// Never returns [types.ErrRunAlreadyCompleted] — Local cannot distinguish finished vs
// unknown; the agent run registry handles in-process terminal checks.
func (rt *LocalRuntime) GetRunHandle(_ context.Context, _ string) (sdkruntime.RunHandle, error) {
	return nil, types.ErrRunNotFound
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

	result, loopErr := rt.RunAgentLoop(runCtx, AgentLoopInput{
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

// GetStreamHandle always returns [types.ErrStreamNotFound]. LocalRuntime does not
// track streams durably; same-process live handles are managed by the agent stream registry.
// After a process crash there is nothing to reconnect to.
// Never returns [types.ErrRunAlreadyCompleted] — Local cannot distinguish finished vs
// unknown; the agent stream registry handles in-process terminal checks.
func (rt *LocalRuntime) GetStreamHandle(_ context.Context, _ string) (sdkruntime.StreamHandle, error) {
	return nil, types.ErrStreamNotFound
}

// approve resolves a pending tool approval registered during a streaming run.
// When a tool requires approval, executeSingleTool registers a token and blocks; the
// caller receives a CUSTOM event on the stream with that token and calls
// [sdkruntime.StreamHandle.Approve] to unblock.
// Returns [types.ErrApprovalAlreadyResolved] when the token is unknown or was already
// resolved (same sentinel as Temporal when CompleteActivity reports not found).
func (rt *LocalRuntime) approve(_ context.Context, approvalToken string, status types.ApprovalStatus) error {
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
func (rt *LocalRuntime) Close() {
	if rt.ownsEventBus && rt.eventbus != nil {
		rt.eventbus.Close()
		rt.ownsEventBus = false
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
