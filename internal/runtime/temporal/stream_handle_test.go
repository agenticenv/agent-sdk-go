package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"
)

// newTestStreamRuntime builds a minimal TemporalRuntime for streamHandle unit tests.
func newTestStreamRuntime(tc client.Client) *TemporalRuntime {
	return &TemporalRuntime{
		Runtime:        base.Runtime{AgentSpec: sdkruntime.AgentSpec{Name: "root"}},
		temporalClient: tc,
		logger:         logger.NoopLogger(),
	}
}

// stubWorkflowStreamSubscribeEnd makes WorkflowStream Subscribe exit with no items (no forwardStreamFn).
// UpdateWorkflow fails; a later DescribeWorkflowExecution must report terminal for a clean exit.
func stubWorkflowStreamSubscribeEnd(tc *temporalmocks.Client) {
	tc.On("UpdateWorkflow", mock.Anything, mock.Anything).
		Return(nil, errors.New("unit test: end stream subscribe")).Maybe()
}

// stubEventsRunningThenSubscribeEnd: Events status gate (running), then Subscribe ends via terminal describe.
func stubEventsRunningThenSubscribeEnd(tc *temporalmocks.Client, workflowID string) {
	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, mock.Anything).
		Return(describeWorkflowRunning(), nil).Once()
	stubWorkflowStreamSubscribeEnd(tc)
	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, mock.Anything).
		Return(describeWorkflowCompleted(), nil).Maybe()
}

func waitHandleDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handle Done timed out")
	}
}

// newTestStreamHandle wires GetWorkflow → wfRun for awaitCompletion, then creates the handle.
func newTestStreamHandle(
	id, workflowID, threadID string,
	tc *temporalmocks.Client,
	rt *TemporalRuntime,
	wfRun *temporalmocks.WorkflowRun,
	cancel context.CancelFunc,
	cleanup func(),
) *streamHandle {
	tc.On("GetWorkflow", mock.Anything, workflowID, "").Return(wfRun).Maybe()
	return newStreamHandle(id, workflowID, threadID, rt, cancel, cleanup)
}

func collectStreamEvents(t *testing.T, ch <-chan events.AgentEvent, timeout time.Duration) []events.AgentEvent {
	t.Helper()
	var out []events.AgentEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if ev != nil {
				out = append(out, ev)
			}
		case <-deadline:
			t.Fatalf("collectStreamEvents: timed out after %s", timeout)
			return out
		}
	}
}

func streamEventTypes(evs []events.AgentEvent) []events.AgentEventType {
	typesOut := make([]events.AgentEventType, len(evs))
	for i, ev := range evs {
		typesOut[i] = ev.Type()
	}
	return typesOut
}

func TestStreamHandle_ID(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.Equal(t, "run-1", h.ID())
}

func TestStreamHandle_Status_Running(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", "").
		Return(describeWorkflowRunning(), nil).Once()

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)
}

func TestStreamHandle_Status_NotFound(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-missing", "").
		Return(nil, errors.New("workflow not found for ID")).Once()

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, nil, nil)
	h := newTestStreamHandle("run-1", "wf-missing", "thread-1", tc, rt, wfRun, nil, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	_, err := h.Status(context.Background())
	require.ErrorIs(t, err, types.ErrRunNotFound)
}

func TestStreamHandle_Status_NilRuntime(t *testing.T) {
	h := newStreamHandle("run-1", "wf-1", "thread-1", nil, nil, nil)
	_, err := h.Status(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no runtime")
}

func TestStreamHandle_Cancel_Running(t *testing.T) {
	release := make(chan struct{})
	var cancelled atomic.Bool
	runCancel := func() { cancelled.Store(true) }

	tc := temporalmocks.NewClient(t)
	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, runCancel, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.NoError(t, h.Cancel(context.Background()))
	require.True(t, cancelled.Load())
}

func TestStreamHandle_Cancel_NilCancel(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestStreamHandle_Cancel_Twice(t *testing.T) {
	release := make(chan struct{})
	runCancel := func() {}

	tc := temporalmocks.NewClient(t)
	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, runCancel, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.NoError(t, h.Cancel(context.Background()))
	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestStreamHandle_Events_NotConfigured(t *testing.T) {
	h := newStreamHandle("run-1", "wf-1", "thread-1", nil, nil, nil)
	_, err := h.Events(context.Background(), 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
}

func TestStreamHandle_Events_NotFound(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", "").
		Return(nil, errors.New("workflow not found for ID")).Once()

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, nil, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	_, err := h.Events(context.Background(), 0)
	require.ErrorIs(t, err, types.ErrRunNotFound)
}

func TestStreamHandle_Events_AlreadyCompleted(t *testing.T) {
	release := make(chan struct{})

	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", "").
		Return(describeWorkflowCompleted(), nil).Once()

	cleaned := false
	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, nil, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, func() { cleaned = true })

	_, err := h.Events(context.Background(), 0)
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
	require.False(t, cleaned, "Events terminal check must not run cleanup")

	close(release)
	waitHandleDone(t, h.Done())
	require.True(t, cleaned, "cleanup should run when awaitCompletion finishes")
}

func TestStreamHandle_Events_EmitsStartedAndFinished(t *testing.T) {
	release := make(chan struct{})
	want := &types.AgentRunResult{Content: "done", AgentName: "root"}

	tc := temporalmocks.NewClient(t)
	stubEventsRunningThenSubscribeEnd(tc, "wf-1")

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, want, nil)

	cleaned := false
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, func() { cleaned = true })

	ch, err := h.Events(context.Background(), 0)
	require.NoError(t, err)

	close(release)
	evs := collectStreamEvents(t, ch, 3*time.Second)
	gotTypes := streamEventTypes(evs)

	require.Contains(t, gotTypes, events.AgentEventTypeRunStarted)
	require.Contains(t, gotTypes, events.AgentEventTypeRunFinished)
	require.Equal(t, events.AgentEventTypeRunStarted, gotTypes[0])
	require.Equal(t, events.AgentEventTypeRunFinished, gotTypes[len(gotTypes)-1])

	waitHandleDone(t, h.Done())
	require.True(t, cleaned, "cleanup should run when awaitCompletion finishes")
}

func TestStreamHandle_Events_OffsetSkipsStarted(t *testing.T) {
	release := make(chan struct{})
	want := &types.AgentRunResult{Content: "done"}

	tc := temporalmocks.NewClient(t)
	stubEventsRunningThenSubscribeEnd(tc, "wf-1")

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, want, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)

	ch, err := h.Events(context.Background(), 42)
	require.NoError(t, err)

	close(release)
	evs := collectStreamEvents(t, ch, 3*time.Second)
	gotTypes := streamEventTypes(evs)

	require.NotContains(t, gotTypes, events.AgentEventTypeRunStarted)
	require.Contains(t, gotTypes, events.AgentEventTypeRunFinished)
}

func TestStreamHandle_Events_WorkflowError(t *testing.T) {
	release := make(chan struct{})
	wantErr := errors.New("stream wf err")

	tc := temporalmocks.NewClient(t)
	stubEventsRunningThenSubscribeEnd(tc, "wf-1")

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, nil, wantErr)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)

	ch, err := h.Events(context.Background(), 0)
	require.NoError(t, err)

	close(release)
	evs := collectStreamEvents(t, ch, 3*time.Second)
	gotTypes := streamEventTypes(evs)

	require.Contains(t, gotTypes, events.AgentEventTypeRunStarted)
	require.Contains(t, gotTypes, events.AgentEventTypeRunError)
}

// TestStreamHandle_Events_RunTimeoutUnblocksWithoutEventsDeadline verifies that when the run
// stops via WithTimeout (stopCause=DeadlineExceeded), Events(Background) still gets RUN_ERROR
// with "context deadline exceeded" — apps must not put a deadline on every Events call.
func TestStreamHandle_Events_RunTimeoutUnblocksWithoutEventsDeadline(t *testing.T) {
	release := make(chan struct{})
	termErr := errors.New("workflow terminated: run timeout")

	tc := temporalmocks.NewClient(t)
	stubEventsRunningThenSubscribeEnd(tc, "wf-1")

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, nil, termErr)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)
	h.setStopCause(context.DeadlineExceeded)

	ch, err := h.Events(context.Background(), 0)
	require.NoError(t, err)

	close(release)
	evs := collectStreamEvents(t, ch, 3*time.Second)
	require.Contains(t, streamEventTypes(evs), events.AgentEventTypeRunError)

	var msg string
	for _, ev := range evs {
		if re, ok := ev.(*events.AgentRunErrorEvent); ok {
			msg = re.Message
		}
	}
	require.Equal(t, "context deadline exceeded", msg)
	waitHandleDone(t, h.Done())
}

// TestStreamHandle_Events_RunCancelUnblocksWithoutEventsDeadline verifies that when the run
// stops via explicit Cancel() (stopCause=Canceled), Events(Background) still gets RUN_ERROR
// with "context canceled" — symmetric to the timeout case.
func TestStreamHandle_Events_RunCancelUnblocksWithoutEventsDeadline(t *testing.T) {
	release := make(chan struct{})
	termErr := errors.New("workflow terminated: run cancelled")

	tc := temporalmocks.NewClient(t)
	stubEventsRunningThenSubscribeEnd(tc, "wf-1")

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, nil, termErr)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)
	h.setStopCause(context.Canceled)

	ch, err := h.Events(context.Background(), 0)
	require.NoError(t, err)

	close(release)
	evs := collectStreamEvents(t, ch, 3*time.Second)
	require.Contains(t, streamEventTypes(evs), events.AgentEventTypeRunError)

	var msg string
	for _, ev := range evs {
		if re, ok := ev.(*events.AgentRunErrorEvent); ok {
			msg = re.Message
		}
	}
	require.Equal(t, "context canceled", msg)
	waitHandleDone(t, h.Done())
}

func TestTerminalStreamErrorMessage(t *testing.T) {
	// Deadline: stopCause wins regardless of getErr content.
	require.Equal(t, "context deadline exceeded",
		terminalStreamErrorMessage(context.DeadlineExceeded, errors.New("terminated")))
	// Deadline: getErr sentinel also triggers clean message.
	require.Equal(t, "context deadline exceeded",
		terminalStreamErrorMessage(nil, context.DeadlineExceeded))
	// Cancel: stopCause wins, raw Temporal string is not surfaced.
	require.Equal(t, "context canceled",
		terminalStreamErrorMessage(context.Canceled, errors.New("workflow terminated")))
	// Cancel: getErr sentinel also triggers clean message.
	require.Equal(t, "context canceled",
		terminalStreamErrorMessage(nil, context.Canceled))
	// No sentinel: raw error from Temporal is passed through.
	require.Equal(t, "workflow failed",
		terminalStreamErrorMessage(nil, errors.New("workflow failed")))
}

// TestStreamHandle_Events_SubscribeErrorStillDeliversTerminal simulates a non-terminal
// Subscribe failure (UpdateWorkflow error while the workflow is still RUNNING). deliverEvents
// must fall through to Get and emit RUN_FINISHED before closing the channel — not exit early
// with a silent channel close.
func TestStreamHandle_Events_SubscribeErrorStillDeliversTerminal(t *testing.T) {
	release := make(chan struct{})
	want := &types.AgentRunResult{Content: "done", AgentName: "root"}

	tc := temporalmocks.NewClient(t)
	// Events() status gate.
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", mock.Anything).
		Return(describeWorkflowRunning(), nil).Once()
	// Subscribe poll fails with a genuine RPC error (ctx still live).
	tc.On("UpdateWorkflow", mock.Anything, mock.Anything).
		Return(nil, errors.New("unit test: non-terminal subscribe RPC failure")).Maybe()
	// followContinueAsNew / isTerminal see RUNNING → Subscribe yields the error (not clean exit).
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", mock.Anything).
		Return(describeWorkflowRunning(), nil).Maybe()

	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, want, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, nil)

	ch, err := h.Events(context.Background(), 0)
	require.NoError(t, err)

	close(release)
	evs := collectStreamEvents(t, ch, 3*time.Second)
	gotTypes := streamEventTypes(evs)

	require.Contains(t, gotTypes, events.AgentEventTypeRunStarted)
	require.Contains(t, gotTypes, events.AgentEventTypeRunFinished,
		"subscribe RPC failure must still deliver a terminal event before channel close")
	require.Equal(t, events.AgentEventTypeRunFinished, gotTypes[len(gotTypes)-1],
		"terminal event must be last before channel close")
	waitHandleDone(t, h.Done())
}

func TestStreamHandle_Events_CleanupOnce(t *testing.T) {
	release := make(chan struct{})
	want := &types.AgentRunResult{Content: "done"}

	tc := temporalmocks.NewClient(t)
	// First Events: running → subscribe end. Later Describes (isTerminal + second Events) → completed.
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", mock.Anything).
		Return(describeWorkflowRunning(), nil).Once()
	stubWorkflowStreamSubscribeEnd(tc)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", mock.Anything).
		Return(describeWorkflowCompleted(), nil).Maybe()

	calls := 0
	rt := newTestStreamRuntime(tc)
	wfRun := blockingWorkflowRun(release, want, nil)
	h := newTestStreamHandle("run-1", "wf-1", "thread-1", tc, rt, wfRun, nil, func() { calls++ })

	ch, err := h.Events(context.Background(), 0)
	require.NoError(t, err)
	close(release)
	_ = collectStreamEvents(t, ch, 3*time.Second)
	waitHandleDone(t, h.Done())
	require.Equal(t, 1, calls)

	_, err = h.Events(context.Background(), 0)
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
	require.Equal(t, 1, calls, "cleanup must run only once from awaitCompletion")
}

func TestSyntheticStreamCompleteEvent(t *testing.T) {
	ev := syntheticStreamCompleteEvent(nil, "threadID", "runID", "root")
	fin, _ := ev.(*events.AgentRunFinishedEvent)
	if ev == nil || ev.Type() != events.AgentEventTypeRunFinished || fin.Result == nil || fin.Result.AgentName != "root" {
		t.Fatalf("nil resp: %+v", ev)
	}

	ev2 := syntheticStreamCompleteEvent(&types.AgentRunResult{
		Content:   "body",
		AgentName: "from-result",
		LLMUsage:  &types.LLMUsage{TotalTokens: 9},
	}, "threadID", "runID", "root")

	fin2, _ := ev2.(*events.AgentRunFinishedEvent)
	result := fin2.Result
	if result == nil {
		t.Fatalf("expected AgentRunResult, got nil")
	}
	if result.LLMUsage == nil {
		t.Fatal("llm usage should be set")
	}
	if result.Content != "body" || result.AgentName != "from-result" || result.LLMUsage.TotalTokens != 9 {
		t.Fatalf("with AgentName: %+v", ev2)
	}

	ev3 := syntheticStreamCompleteEvent(&types.AgentRunResult{Content: "c", AgentName: ""}, "threadID", "runID", "fallback")
	fin3, _ := ev3.(*events.AgentRunFinishedEvent)
	result = fin3.Result
	if result == nil {
		t.Fatalf("expected AgentRunResult, got nil")
	}
	if result.AgentName != "fallback" {
		t.Fatalf("fallback name: got %q", result.AgentName)
	}

	ev4 := syntheticStreamCompleteEvent(&types.AgentRunResult{Content: "only"}, "threadID", "runID", "")
	fin4, _ := ev4.(*events.AgentRunFinishedEvent)
	result = fin4.Result
	if result == nil {
		t.Fatalf("expected AgentRunResult, got nil")
	}
	if result.AgentName != "" {
		t.Fatalf("empty rootName with empty resp.AgentName: got %q", result.AgentName)
	}
}

// TestApprovalToolCallIDOf verifies extraction from tool_approval and sub_agent_delegation events.
func TestApprovalToolCallIDOf(t *testing.T) {
	toolEv := events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval),
		events.AgentCustomEventApprovalValue{ToolCallID: "tc-tool", ToolName: "calc", ApprovalToken: "tok"})
	delegEv := events.NewAgentCustomEvent(string(events.AgentCustomEventNameSubAgentDelegation),
		events.AgentCustomEventDelegationValue{ToolCallID: "tc-deleg", SubAgentName: "sub", ApprovalToken: "tok2"})
	otherEv := events.NewAgentCustomEvent("other", nil)
	textEv := events.NewAgentTextMessageStartEvent("msg-1", "assistant")

	if id, ok := approvalToolCallIDOf(toolEv); !ok || id != "tc-tool" {
		t.Errorf("tool approval: got (%q, %v), want (tc-tool, true)", id, ok)
	}
	if id, ok := approvalToolCallIDOf(delegEv); !ok || id != "tc-deleg" {
		t.Errorf("delegation approval: got (%q, %v), want (tc-deleg, true)", id, ok)
	}
	if _, ok := approvalToolCallIDOf(otherEv); ok {
		t.Error("other custom event: expected (_, false)")
	}
	if _, ok := approvalToolCallIDOf(textEv); ok {
		t.Error("text event: expected (_, false)")
	}
}

// TestMessageIDOf verifies that messageIDOf extracts IDs from text/reasoning events and returns
// "" for unrelated event types.
func TestMessageIDOf(t *testing.T) {
	cases := []struct {
		ev   events.AgentEvent
		want string
	}{
		{events.NewAgentTextMessageStartEvent("msg-1", "assistant"), "msg-1"},
		{events.NewAgentTextMessageContentEvent("msg-2", "hello"), "msg-2"},
		{events.NewAgentTextMessageEndEvent("msg-3"), "msg-3"},
		{events.NewAgentToolCallStartEvent("tc1", "echo"), ""},
		{events.NewAgentRunStartedEvent("run-1", "run-1"), ""},
	}
	for _, tc := range cases {
		got := messageIDOf(tc.ev)
		if got != tc.want {
			t.Errorf("messageIDOf(%T) = %q, want %q", tc.ev, got, tc.want)
		}
	}
}
