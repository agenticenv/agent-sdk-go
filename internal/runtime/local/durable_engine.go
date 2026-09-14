package local

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	durable "github.com/agenticenv/durable-go"
)

// defaultAutoPurgeAge and defaultAutoPurgeInterval are LocalConfig's documented
// zero-value auto-purge defaults (7 days / 1 hour) when durability is enabled and the
// caller does not construct its own [durable.Engine].
const (
	defaultAutoPurgeAge      = 7 * 24 * time.Hour
	defaultAutoPurgeInterval = time.Hour
)

// durableRunInput is the JSON resume DTO durable-go persists to input.json on first
// start. Everything a resumed Task.Exec needs that IS serializable lives here; anything
// that is not (Tools, SubAgentRoutes, a per-request ApprovalHandler override, the
// eventbus ChannelName) lives in [liveRunExtras] instead — see durableExec.
type durableRunInput struct {
	UserPrompt       string                  `json:"user_prompt"`
	ConversationID   string                  `json:"conversation_id,omitempty"`
	IsStream         bool                    `json:"is_stream"`
	StreamingEnabled bool                    `json:"streaming_enabled"`
	EventTypes       []events.AgentEventType `json:"event_types,omitempty"`
	MaxSubAgentDepth int                     `json:"max_sub_agent_depth"`
	MemoryScope      interfaces.MemoryScope  `json:"memory_scope"`
	// Budget carries the *configuration* (limits), not tracker state. Replay
	// deterministically re-accumulates totals: every already-completed LLM-call RunStep
	// is a cache hit that returns the same cached usage, so calling BudgetTracker.Add in
	// the same order on a fresh tracker reproduces the exact same running totals — no
	// separate tracker-state persistence is needed. See base.BudgetTracker.
	Budget *types.BudgetConfig `json:"budget,omitempty"`
}

// durableRunOutput is the JSON DTO durable-go persists to output.json on completion.
type durableRunOutput struct {
	Content   string                `json:"content"`
	LLMUsage  *interfaces.LLMUsage  `json:"llm_usage,omitempty"`
	Telemetry *types.AgentTelemetry `json:"telemetry,omitempty"`
	AgentName string                `json:"agent_name"`
	Model     string                `json:"model"`
	Metadata  map[string]any        `json:"metadata,omitempty"`
}

// liveRunExtras holds the non-JSON-serializable per-run wiring for a run started in this
// process (Tools, SubAgentRoutes, the eventbus channel name). ApprovalHandler is not
// included: neither Run nor Stream take a per-request override today, so durableExec
// always uses the runtime's static rt.approvalHandler. Stored on [LocalRuntime.liveRuns]
// keyed by runID just before durable.RunTask is called, and read back by durableExec
// when it actually executes. Not present after a process restart — see durableExec's
// fallback path.
type liveRunExtras struct {
	tools          []interfaces.Tool
	subAgentRoutes map[string]subAgentRoute
	channelName    string
}

// setupDurability resolves this runtime's durable-go engine from localConfig (or leaves
// engine nil for the original in-memory path) and registers this agent's Task. Called
// once from NewLocalRuntime, after options are applied.
func (rt *LocalRuntime) setupDurability() error {
	cfg := rt.localConfig
	if !cfg.durabilityEnabled() {
		return nil
	}

	if cfg != nil && cfg.Engine != nil {
		rt.engine = cfg.Engine
		rt.ownsEngine = false
	} else {
		dataDir := ""
		if cfg != nil {
			dataDir = cfg.DataDir
		}
		if dataDir == "" {
			dataDir = defaultDataDir(rt.AgentSpec.Name)
		}

		opts := []durable.EngineOption{durable.WithAutoPurge(defaultAutoPurgeAge, defaultAutoPurgeInterval)}
		if cfg != nil {
			if cfg.AutoPurgeAge < 0 {
				opts = nil // negative disables auto-purge: omit the option (durable-go default is off)
			} else if cfg.AutoPurgeAge > 0 || cfg.AutoPurgeInterval > 0 {
				age := cfg.AutoPurgeAge
				if age <= 0 {
					age = defaultAutoPurgeAge
				}
				interval := cfg.AutoPurgeInterval
				if interval <= 0 {
					interval = defaultAutoPurgeInterval
				}
				opts = []durable.EngineOption{durable.WithAutoPurge(age, interval)}
			}
			if cfg.MaxRetries > 0 {
				opts = append(opts, durable.WithMaxRetries(cfg.MaxRetries))
			}
			if cfg.Timeout > 0 {
				opts = append(opts, durable.WithTimeout(cfg.Timeout))
			}
			if cfg.LockTimeout > 0 {
				opts = append(opts, durable.WithLockTimeout(cfg.LockTimeout))
			}
		}

		engine, err := durable.NewEngine(context.Background(), dataDir, opts...)
		if err != nil {
			return fmt.Errorf("local: durable engine construction failed (dataDir %q): %w", dataDir, err)
		}
		rt.engine = engine
		rt.ownsEngine = true
	}

	rt.taskID = sanitizeDurableID(rt.AgentSpec.Name)
	if err := durable.RegisterTask[durableRunInput, durableRunOutput](rt.engine, rt.taskID, durable.Func(rt.durableExec)); err != nil {
		return fmt.Errorf("local: durable RegisterTask(%q) failed: %w", rt.taskID, err)
	}
	return nil
}

// storeLiveRunExtras records the non-serializable per-run wiring for runID just before
// durable.RunTask is called, so durableExec can look it up when it actually executes.
func (rt *LocalRuntime) storeLiveRunExtras(runID string, tools []interfaces.Tool, subAgents []*sdkruntime.SubAgentSpec, channelName string) {
	rt.liveRuns.Store(runID, &liveRunExtras{
		tools:          tools,
		subAgentRoutes: buildSubAgentRoutes(subAgents),
		channelName:    channelName,
	})
}

// durableExec is this runtime's durable.Task[durableRunInput, durableRunOutput].Exec. It
// is registered exactly once (setupDurability) and invoked by durable-go once per run —
// on first start, on every process-restart resume, and on any in-process reattach
// (GetRunHandle/GetStreamHandle after the original goroutine's liveRuns entry was
// already cleaned up). ctx is durable-go's run ctx: cancelled by durable.CancelRun or a
// task/run timeout, NOT by the ctx passed to RunTask (see package durable's RunTask doc)
// — this is why RunHandle.Cancel must call durable.CancelRun explicitly (run_handle.go).
func (rt *LocalRuntime) durableExec(ctx context.Context, s *durable.StepRunner, in durableRunInput) (durableRunOutput, error) {
	runID := s.RunID()
	defer rt.liveRuns.Delete(runID)

	var tools []interfaces.Tool
	var subAgentRoutes map[string]subAgentRoute
	approvalHandler := rt.approvalHandler
	channelName := ""
	if in.IsStream {
		channelName = localChannelName(runID)
	}

	if v, ok := rt.liveRuns.Load(runID); ok {
		extras := v.(*liveRunExtras)
		tools = extras.tools
		subAgentRoutes = extras.subAgentRoutes
		if extras.channelName != "" {
			channelName = extras.channelName
		}
	} else if rt.toolsResolver != nil {
		// Resumed after a process restart (or a reattach with no live extras left):
		// rehydrate what a Runtime.GetRunHandle(ctx, runID)-shaped API can still give us.
		// SubAgentRoutes cannot be recovered this way (no request payload survives a
		// process restart on this interface) — a sub-agent delegation tool call the LLM
		// attempts on a resumed run falls back to the existing
		// "Sub-agent delegation not available for this runtime" message.
		resolved, err := rt.toolsResolver(ctx)
		if err != nil {
			rt.logger.Warn(ctx, "local: durable resume tools resolver failed; continuing with no tools",
				slog.String("scope", "runtime"),
				slog.String("runID", runID),
				slog.Any("error", err))
		} else {
			tools = resolved
		}
	}

	eventTypes := []events.AgentEventType{}
	if !in.IsStream && approvalHandler != nil {
		eventTypes = []events.AgentEventType{events.AgentEventTypeCustom}
	} else if in.IsStream {
		eventTypes = in.EventTypes
		if len(eventTypes) == 0 {
			eventTypes = []events.AgentEventType{events.AgentEventAll}
		}
	}

	budgetTracker := base.NewBudgetTracker(in.Budget)
	var seq atomic.Int64
	loopResult, err := rt.executeAgentLoop(ctx, AgentLoopInput{
		UserPrompt:        in.UserPrompt,
		RunID:             runID,
		ConversationID:    in.ConversationID,
		MemoryScope:       in.MemoryScope,
		StreamingEnabled:  in.StreamingEnabled,
		ChannelName:       channelName,
		EventTypes:        eventTypes,
		ApprovalHandler:   approvalHandler,
		SubAgentRoutes:    subAgentRoutes,
		SubAgentDepth:     0,
		MaxSubAgentDepth:  in.MaxSubAgentDepth,
		Tools:             tools,
		BudgetTracker:     budgetTracker,
		EnforceBudget:     budgetTracker != nil,
		stepRunner:        s,
		stepPrefix:        "",
		budgetApprovalSeq: &seq,
	})
	if err != nil {
		return durableRunOutput{}, err
	}

	return durableRunOutput{
		Content:   loopResult.Content,
		LLMUsage:  loopResult.LLMUsage,
		Telemetry: loopResult.Telemetry,
		AgentName: rt.AgentSpec.Name,
		Model:     rt.AgentConfig.LLM.Client.GetModel(),
		Metadata:  map[string]any{},
	}, nil
}

// runOptions returns the durable.RunOption(s) for one RunTask call, propagating the
// agent's Limits.Timeout as this run's durable-go deadline so the two stay consistent
// (otherwise a local Limits.Timeout wrapper context could give up client-side while the
// durable run keeps executing unbounded in the background — see LocalConfig.Timeout doc).
func (rt *LocalRuntime) runOptions() []durable.RunOption {
	if d := rt.AgentConfig.Limits.Timeout; d > 0 {
		return []durable.RunOption{durable.WithRunTimeout(d)}
	}
	return nil
}

// startDurableRun starts (or resumes/reattaches) runID via durable.RunTask and returns
// its handle. dto is only used on a genuine first start — durable.RunTask ignores it and
// reloads input.json for an existing runID (resume/reattach).
func (rt *LocalRuntime) startDurableRun(runCtx context.Context, runID string, dto durableRunInput) *durable.TaskRun[durableRunOutput] {
	return durable.RunTask[durableRunInput, durableRunOutput](runCtx, rt.engine, rt.taskID, runID, dto, rt.runOptions()...)
}

// driveDurableRun drives a durable-go run to completion for a [runHandle], mirroring
// [LocalRuntime.driveRun] but delegating actual execution to durable.RunTask/durableExec
// instead of calling executeAgentLoop directly. Used by both a fresh Run() and a
// GetRunHandle reattach after a process restart (dto is then a zero value; the real
// input is reloaded from disk — see startDurableRun).
func (rt *LocalRuntime) driveDurableRun(runCtx context.Context, dto durableRunInput, handle *runHandle) {
	out, err := rt.startDurableRun(runCtx, handle.id, dto).Get(runCtx)
	if err != nil {
		rt.logger.Error(runCtx, "runtime durable run failed",
			slog.String("scope", "runtime"),
			slog.String("runID", handle.id),
			slog.Any("error", err))
		handle.markDone(nil, err)
		return
	}
	handle.markDone(&types.AgentRunResult{
		Content:   out.Content,
		AgentName: out.AgentName,
		Model:     out.Model,
		Metadata:  out.Metadata,
		RunID:     handle.id,
		LLMUsage:  out.LLMUsage,
		Telemetry: out.Telemetry,
	}, nil)
}

// replayStepHistory publishes one [events.AgentCustomEventNameStepReplayed] event per
// already-completed or already-failed step recorded for runID, in StartedAt order, onto
// channel. Used by GetStreamHandle so a reconnecting subscriber sees a coarse summary of
// everything that happened before the crash/disconnect, before live events resume for
// whatever the run does next. A running/waiting step (in-flight or a still-open
// approval) is intentionally skipped — durableExec's re-drive will reach it and either
// replay it from cache (nothing new to show) or continue it live (a normal CUSTOM/step
// event fires then). Errors are logged and otherwise ignored: a failed history replay
// must not block reconnecting to live events.
func (rt *LocalRuntime) replayStepHistory(ctx context.Context, runID, channel string) {
	steps, err := rt.engine.LoadSteps(ctx, rt.taskID, runID)
	if err != nil {
		rt.logger.Warn(ctx, "local: durable step history replay failed",
			slog.String("scope", "runtime"),
			slog.String("runID", runID),
			slog.Any("error", err))
		return
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].StartedAt.Before(steps[j].StartedAt) })
	for _, rec := range steps {
		if rec.Status != durable.StepStatusCompleted && rec.Status != durable.StepStatusFailed {
			continue
		}
		value := events.AgentCustomEventStepReplayedValue{
			StepID:      rec.StepID,
			Status:      string(rec.Status),
			Result:      json.RawMessage(rec.Result),
			StartedAt:   rec.StartedAt.Format(time.RFC3339Nano),
			CompletedAt: rec.CompletedAt.Format(time.RFC3339Nano),
		}
		rt.publishEventToChannel(ctx, channel, events.NewAgentCustomEvent(string(events.AgentCustomEventNameStepReplayed), value))
	}
}

// driveDurableStream is [driveDurableRun]'s streaming counterpart: same durable.RunTask
// execution, plus the RUN_FINISHED/RUN_ERROR lifecycle events [LocalRuntime.driveStream]
// publishes. emitStarted controls RUN_STARTED — true for a fresh Stream() call, false for
// a GetStreamHandle reattach (the original caller already saw RUN_STARTED before the
// crash; re-emitting it on reconnect would be confusing).
func (rt *LocalRuntime) driveDurableStream(runCtx context.Context, dto durableRunInput, handle *streamHandle, channel, threadID string, emitStarted bool) {
	if emitStarted {
		rt.publishLifecycleEvent(channel, events.NewAgentRunStartedEvent(threadID, handle.id))
	}
	out, err := rt.startDurableRun(runCtx, handle.id, dto).Get(runCtx)
	if err != nil {
		rt.logger.Error(runCtx, "runtime durable stream failed",
			slog.String("scope", "runtime"),
			slog.String("runID", handle.id),
			slog.Any("error", err))
		rt.publishLifecycleEvent(channel, events.NewAgentRunErrorEvent(err.Error()))
		handle.markDone(nil, err)
		return
	}
	result := &types.AgentRunResult{
		Content:   out.Content,
		AgentName: out.AgentName,
		Model:     out.Model,
		Metadata:  out.Metadata,
		RunID:     handle.id,
		LLMUsage:  out.LLMUsage,
		Telemetry: out.Telemetry,
	}
	rt.publishLifecycleEvent(channel, events.NewAgentRunFinishedEvent(threadID, handle.id, result))
	handle.markDone(result, nil)
}
