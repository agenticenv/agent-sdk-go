package local

import (
	"context"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	durable "github.com/agenticenv/durable-go"
	"github.com/stretchr/testify/require"
)

// newDurableRT builds a LocalRuntime with durability on (default), rooted at a fresh
// per-test temp DataDir so tests never share journal state or an OS-level DataDir lock.
func newDurableRT(t *testing.T, client interfaces.LLMClient, agentName string) *LocalRuntime {
	t.Helper()
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: agentName, SystemPrompt: "sys"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:    sdkruntime.AgentLLM{Client: client},
			Limits: sdkruntime.AgentLimits{MaxIterations: 5, Timeout: 30 * time.Second},
		}),
		WithLocalConfig(&LocalConfig{DataDir: t.TempDir()}),
	)
	require.NoError(t, err)
	t.Cleanup(rt.Close)
	return rt
}

// ---------------------------------------------------------------------------
// Durable-by-default / opt-out
// ---------------------------------------------------------------------------

func TestDurable_OnByDefault(t *testing.T) {
	rt := newDurableRT(t, &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "hi there"}}}, "durable-agent")
	require.NotNil(t, rt.engine, "engine should be constructed when Durability is unset (default true)")
	require.True(t, rt.ownsEngine)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hello"})
	require.NoError(t, err)
	result, err := handle.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "hi there", result.Content)

	// Durable-go persisted a completed run for this taskID/runID.
	info, ok, err := rt.engine.GetTask(context.Background(), rt.taskID, handle.ID())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, durable.StatusCompleted, info.Status)
}

func TestDurable_ExplicitOptOut(t *testing.T) {
	off := false
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "opt-out-agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:    sdkruntime.AgentLLM{Client: &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "ok"}}}},
			Limits: sdkruntime.AgentLimits{MaxIterations: 5, Timeout: 5 * time.Second},
		}),
		WithLocalConfig(&LocalConfig{Durability: &off}),
	)
	require.NoError(t, err)
	defer rt.Close()
	require.Nil(t, rt.engine, "Durability=false must leave engine nil")

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)
	result, err := handle.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "ok", result.Content)

	// No durable reconnect available on the non-durable path.
	_, err = rt.GetRunHandle(context.Background(), handle.ID())
	require.ErrorIs(t, err, types.ErrRunNotFound)
}

func TestDurable_CallerSuppliedEngineNotClosed(t *testing.T) {
	engine, err := durable.NewEngine(context.Background(), t.TempDir())
	require.NoError(t, err)
	defer func() { _ = engine.Close() }()

	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "shared-engine-agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:    sdkruntime.AgentLLM{Client: &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "done"}}}},
			Limits: sdkruntime.AgentLimits{MaxIterations: 5, Timeout: 5 * time.Second},
		}),
		WithLocalConfig(&LocalConfig{Engine: engine}),
	)
	require.NoError(t, err)
	require.Same(t, engine, rt.engine)
	require.False(t, rt.ownsEngine)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)
	_, err = handle.Get(context.Background())
	require.NoError(t, err)

	rt.Close()
	// Engine must still be usable after rt.Close() — caller owns its lifecycle.
	_, _, err = engine.GetTask(context.Background(), rt.taskID, handle.ID())
	require.NoError(t, err)
}

func TestSlogFromLogger(t *testing.T) {
	require.Nil(t, slogFromLogger(nil))
	require.NotNil(t, slogFromLogger(logger.NoopLogger()))
	require.Nil(t, slogFromLogger(stubLogger{}))
}

type stubLogger struct{}

func (stubLogger) Debug(context.Context, string, ...any) {}
func (stubLogger) Info(context.Context, string, ...any)  {}
func (stubLogger) Warn(context.Context, string, ...any)  {}
func (stubLogger) Error(context.Context, string, ...any) {}

func TestDurable_AutoPurgeCapsConstructEngine(t *testing.T) {
	client := &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "ok"}}}
	cases := []struct {
		name string
		cfg  LocalConfig
	}{
		{name: "max-runs", cfg: LocalConfig{AutoPurgeMaxRuns: 10}},
		{name: "max-bytes", cfg: LocalConfig{AutoPurgeMaxBytes: 1 << 20}},
		{name: "age-off-with-max-runs", cfg: LocalConfig{AutoPurgeAge: -1, AutoPurgeMaxRuns: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.DataDir = t.TempDir()
			rt, err := NewLocalRuntime(
				WithLogger(logger.NoopLogger()),
				WithAgentSpec(sdkruntime.AgentSpec{Name: "purge-caps-" + tc.name}),
				WithAgentConfig(sdkruntime.AgentConfig{
					LLM:    sdkruntime.AgentLLM{Client: client},
					Limits: sdkruntime.AgentLimits{MaxIterations: 5, Timeout: 5 * time.Second},
				}),
				WithLocalConfig(&tc.cfg),
			)
			require.NoError(t, err)
			t.Cleanup(rt.Close)
			require.NotNil(t, rt.engine)
			require.True(t, rt.ownsEngine)
		})
	}
}

// ---------------------------------------------------------------------------
// GetRunHandle / GetStreamHandle lookup semantics
// ---------------------------------------------------------------------------

func TestDurable_GetRunHandle_UnknownAndEmpty(t *testing.T) {
	rt := newDurableRT(t, &seqLLMClient{}, "lookup-agent")

	_, err := rt.GetRunHandle(context.Background(), "unknown-run-id")
	require.ErrorIs(t, err, types.ErrRunNotFound)

	_, err = rt.GetRunHandle(context.Background(), "")
	require.ErrorIs(t, err, types.ErrRunNotFound, "empty runID must never reach durable.RunTask")
}

func TestDurable_GetRunHandle_AlreadyCompleted(t *testing.T) {
	rt := newDurableRT(t, &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "done"}}}, "completed-agent")

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)
	_, err = handle.Get(context.Background())
	require.NoError(t, err)

	_, err = rt.GetRunHandle(context.Background(), handle.ID())
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
}

func TestDurable_GetStreamHandle_UnknownEmptyAndCompleted(t *testing.T) {
	rt := newDurableRT(t, &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "done"}}}, "stream-lookup-agent")

	_, err := rt.GetStreamHandle(context.Background(), "unknown")
	require.ErrorIs(t, err, types.ErrStreamNotFound)

	_, err = rt.GetStreamHandle(context.Background(), "")
	require.ErrorIs(t, err, types.ErrStreamNotFound)

	handle, err := rt.Stream(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)
	ch, err := handle.Events(context.Background(), 0)
	require.NoError(t, err)
	_ = collectEvents(t, ch, 5*time.Second)
	waitHandleStatus(t, handle, types.StatusCompleted, 2*time.Second)

	_, err = rt.GetStreamHandle(context.Background(), handle.ID())
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
}

// ---------------------------------------------------------------------------
// Approval via durable.CompleteStep
// ---------------------------------------------------------------------------

func TestDurable_ApprovalRoundTrip(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "guarded"}}},
			{Content: "approved and done"},
		},
	}
	tool := stubTool{name: "guarded", result: "ran", needsApproval: true}
	rt := newDurableRT(t, client, "approval-agent")

	handle, err := rt.Stream(context.Background(), &sdkruntime.RunRequest{
		UserPrompt: "run guarded tool",
		Tools:      []interfaces.Tool{tool},
	})
	require.NoError(t, err)
	ch, err := handle.Events(context.Background(), 0)
	require.NoError(t, err)

	var token string
	timeout := time.After(5 * time.Second)
outer:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break outer
			}
			if ev != nil && ev.Type() == events.AgentEventTypeCustom {
				if val, perr := events.ParseCustomEventApproval(ev.(*events.AgentCustomEvent)); perr == nil && val.ApprovalToken != "" {
					token = val.ApprovalToken
					require.NoError(t, rt.approve(context.Background(), token, types.ApprovalStatusApproved))
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for approval token")
		}
	}

	require.NotEmpty(t, token)

	st, err := handle.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCompleted, st)
}

func TestDurable_Approve_AlreadyResolved(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "guarded"}}},
			{Content: "done"},
		},
	}
	tool := stubTool{name: "guarded", result: "ran", needsApproval: true}
	rt := newDurableRT(t, client, "approval-double-agent")

	handle, err := rt.Stream(context.Background(), &sdkruntime.RunRequest{
		UserPrompt: "run",
		Tools:      []interfaces.Tool{tool},
	})
	require.NoError(t, err)
	ch, err := handle.Events(context.Background(), 0)
	require.NoError(t, err)

	var token string
	for token == "" {
		ev := <-ch
		if ev != nil && ev.Type() == events.AgentEventTypeCustom {
			if val, perr := events.ParseCustomEventApproval(ev.(*events.AgentCustomEvent)); perr == nil {
				token = val.ApprovalToken
			}
		}
	}
	require.NoError(t, rt.approve(context.Background(), token, types.ApprovalStatusApproved))
	_ = collectEvents(t, ch, 5*time.Second)

	err = rt.approve(context.Background(), token, types.ApprovalStatusApproved)
	require.ErrorIs(t, err, types.ErrApprovalAlreadyResolved)
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

// waitForDurableTask polls until the engine has persisted meta.json for runID (a live
// handle's local Status field flips to Running immediately on creation, before
// durable.RunTask's own goroutine has actually written anything to disk — Cancel needs
// the on-disk record to exist).
func waitForDurableTask(t *testing.T, engine *durable.Engine, taskID, runID string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if _, ok, err := engine.GetTask(context.Background(), taskID, runID); err == nil && ok {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for durable task %s/%s to appear", taskID, runID)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestDurable_Cancel_MidRun(t *testing.T) {
	blocking := &blockingLLMClient{block: make(chan struct{})}
	rt := newDurableRT(t, blocking, "cancel-agent")

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)
	waitForDurableTask(t, rt.engine, rt.taskID, handle.ID(), 2*time.Second)

	require.NoError(t, handle.Cancel(context.Background()))

	_, getErr := handle.Get(context.Background())
	require.Error(t, getErr)

	st, err := handle.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCancelled, st)

	// handle.Get() can return as soon as the local ctx is cancelled — durable-go's own
	// RunTask.Get "return[s] promptly regardless" of whether the run's background
	// goroutine has actually finished persisting its terminal meta.json (cancellation is
	// cooperative; see durable.Engine.CancelRun's doc). Poll briefly for that on-disk
	// state to settle instead of asserting it is already there.
	require.Eventually(t, func() bool {
		info, ok, err := rt.engine.GetTask(context.Background(), rt.taskID, handle.ID())
		return err == nil && ok && info.Status == durable.StatusFailed
	}, 2*time.Second, 5*time.Millisecond, "durable-go should eventually persist the cancelled run as Failed")

	info, _, err := rt.engine.GetTask(context.Background(), rt.taskID, handle.ID())
	require.NoError(t, err)
	require.Equal(t, durable.ErrRunCancelled.Error(), info.Error)
}

func TestDurable_Cancel_AlreadyCompleted(t *testing.T) {
	rt := newDurableRT(t, &seqLLMClient{responses: []*interfaces.LLMResponse{{Content: "done"}}}, "cancel-completed-agent")

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)
	_, err = handle.Get(context.Background())
	require.NoError(t, err)

	err = handle.Cancel(context.Background())
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
}

func TestDurable_Cancel_SharedEngineSiblingIsolation(t *testing.T) {
	engine, err := durable.NewEngine(context.Background(), t.TempDir())
	require.NoError(t, err)
	defer func() { _ = engine.Close() }()

	blockingA := &blockingLLMClient{block: make(chan struct{})}
	rtA, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:    sdkruntime.AgentLLM{Client: blockingA},
			Limits: sdkruntime.AgentLimits{MaxIterations: 1, Timeout: 30 * time.Second},
		}),
		WithLocalConfig(&LocalConfig{Engine: engine}),
	)
	require.NoError(t, err)

	blockingB := &blockingLLMClient{block: make(chan struct{})}
	rtB, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-b"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:    sdkruntime.AgentLLM{Client: blockingB},
			Limits: sdkruntime.AgentLimits{MaxIterations: 1, Timeout: 30 * time.Second},
		}),
		WithLocalConfig(&LocalConfig{Engine: engine}),
	)
	require.NoError(t, err)

	handleA, err := rtA.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "a"})
	require.NoError(t, err)
	handleB, err := rtB.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "b"})
	require.NoError(t, err)

	waitForDurableTask(t, engine, rtA.taskID, handleA.ID(), 2*time.Second)
	waitForDurableTask(t, engine, rtB.taskID, handleB.ID(), 2*time.Second)

	require.NoError(t, handleA.Cancel(context.Background()))
	_, err = handleA.Get(context.Background())
	require.Error(t, err)
	stA, _ := handleA.Status(context.Background())
	require.Equal(t, types.StatusCancelled, stA)

	// Sibling run on the same Engine, different task, must be unaffected.
	stB, err := handleB.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, stB, "cancelling one task's run must not cancel a sibling task's run on a shared Engine")

	require.NoError(t, handleB.Cancel(context.Background()))
	_, _ = handleB.Get(context.Background())
}

// ---------------------------------------------------------------------------
// Stream reconnect / step replay
// ---------------------------------------------------------------------------

func TestDurable_GetStreamHandle_ReplaysCompletedStepsThenContinuesLive(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "guarded"}}},
			{Content: "final answer"},
		},
	}
	tool := stubTool{name: "guarded", result: "ran", needsApproval: true}
	rt := newDurableRT(t, client, "reconnect-agent")

	handle, err := rt.Stream(context.Background(), &sdkruntime.RunRequest{
		UserPrompt: "go",
		Tools:      []interfaces.Tool{tool},
	})
	require.NoError(t, err)
	ch, err := handle.Events(context.Background(), 0)
	require.NoError(t, err)

	// Drain until the approval CUSTOM event (LLM step already completed by then, so a
	// GetStreamHandle reconnect now should replay it).
	var token string
	for token == "" {
		ev := <-ch
		if ev != nil && ev.Type() == events.AgentEventTypeCustom {
			if val, perr := events.ParseCustomEventApproval(ev.(*events.AgentCustomEvent)); perr == nil {
				token = val.ApprovalToken
			}
		}
	}
	require.NotEmpty(t, token)

	// Reconnect while the run is still Waiting on the approval step.
	handle2, err := rt.GetStreamHandle(context.Background(), handle.ID())
	require.NoError(t, err)
	ch2, err := handle2.Events(context.Background(), 0)
	require.NoError(t, err)

	// The reconnect subscriber must see a step_replayed event for the already-completed
	// LLM step before anything else new happens.
	sawReplay := false
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = rt.approve(context.Background(), token, types.ApprovalStatusApproved)
	}()

	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch2:
			if !ok {
				goto done
			}
			if ev != nil && ev.Type() == events.AgentEventTypeCustom {
				if custom, ok := ev.(*events.AgentCustomEvent); ok && custom.Name == string(events.AgentCustomEventNameStepReplayed) {
					sawReplay = true
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for reconnected stream to finish")
		}
	}
done:
	require.True(t, sawReplay, "GetStreamHandle reconnect must replay the already-completed LLM step")
	_ = collectEvents(t, ch, 5*time.Second) // drain original subscriber too
}

// TestDurable_GetStreamHandle_SameProcessReconnect_NoDuplicateFinished reproduces the
// "double driver" bug: GetStreamHandle called while the original Stream() is still live
// (waiting on an approval step) used to start a second durable.RunTask driver via
// driveDurableStream, so both the original and the reconnecting subscriber ended up
// receiving RUN_FINISHED twice. With registerDriver, the reconnect attaches to the
// already-live driver instead of starting its own — each subscriber must see exactly
// one RUN_FINISHED.
func TestDurable_GetStreamHandle_SameProcessReconnect_NoDuplicateFinished(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "guarded"}}},
			{Content: "final answer"},
		},
	}
	tool := stubTool{name: "guarded", result: "ran", needsApproval: true}
	rt := newDurableRT(t, client, "double-driver-agent")

	handle, err := rt.Stream(context.Background(), &sdkruntime.RunRequest{
		UserPrompt: "go",
		Tools:      []interfaces.Tool{tool},
	})
	require.NoError(t, err)
	ch, err := handle.Events(context.Background(), 0)
	require.NoError(t, err)

	// Drain until the approval CUSTOM event, same as the replay test above.
	var token string
	for token == "" {
		ev := <-ch
		if ev != nil && ev.Type() == events.AgentEventTypeCustom {
			if val, perr := events.ParseCustomEventApproval(ev.(*events.AgentCustomEvent)); perr == nil {
				token = val.ApprovalToken
			}
		}
	}
	require.NotEmpty(t, token)

	// Reconnect while Stream()'s own driver is still live (Waiting on the approval
	// step) — exactly the scenario the reviewer flagged.
	handle2, err := rt.GetStreamHandle(context.Background(), handle.ID())
	require.NoError(t, err)
	ch2, err := handle2.Events(context.Background(), 0)
	require.NoError(t, err)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = rt.approve(context.Background(), token, types.ApprovalStatusApproved)
	}()

	evs1 := collectEvents(t, ch, 5*time.Second)
	evs2 := collectEvents(t, ch2, 5*time.Second)

	require.Equal(t, 1, countFinishedEvents(evs1), "original subscriber must see exactly one RUN_FINISHED")
	require.Equal(t, 1, countFinishedEvents(evs2), "reconnected subscriber must see exactly one RUN_FINISHED")
}

// countFinishedEvents counts RUN_FINISHED events in evs.
func countFinishedEvents(evs []events.AgentEvent) int {
	n := 0
	for _, ev := range evs {
		if ev != nil && ev.Type() == events.AgentEventTypeRunFinished {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Tools resolver rehydration
// ---------------------------------------------------------------------------

func TestDurable_ToolsResolver_UsedOnReattach(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "resolved-tool"}}},
			{Content: "used resolver"},
		},
	}
	tool := stubTool{name: "resolved-tool", result: "42"}

	var resolverCalls int
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "resolver-agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:    sdkruntime.AgentLLM{Client: client},
			Limits: sdkruntime.AgentLimits{MaxIterations: 5, Timeout: 10 * time.Second},
		}),
		WithLocalConfig(&LocalConfig{DataDir: t.TempDir()}),
		WithToolsResolver(func(ctx context.Context) ([]interfaces.Tool, error) {
			resolverCalls++
			return []interfaces.Tool{tool}, nil
		}),
	)
	require.NoError(t, err)
	defer rt.Close()

	// Run() supplies live Tools directly — resolver must not be consulted for a fresh run.
	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{
		UserPrompt: "go",
		Tools:      []interfaces.Tool{tool},
	})
	require.NoError(t, err)
	result, err := handle.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "used resolver", result.Content)
	require.Equal(t, 0, resolverCalls, "a live run must use req.Tools, not the resolver")
}
