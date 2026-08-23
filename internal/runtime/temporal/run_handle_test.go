package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"
)

// blockingWorkflowRun returns a WorkflowRun whose Get blocks until release is closed,
// then writes want (or returns getErr).
func blockingWorkflowRun(release <-chan struct{}, want *types.AgentRunResult, getErr error) *temporalmocks.WorkflowRun {
	wfRun := &temporalmocks.WorkflowRun{}
	wfRun.On("Get", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		<-release
		if getErr != nil {
			return
		}
		if p, ok := args.Get(1).(**types.AgentRunResult); ok && p != nil {
			*p = want
		}
	}).Return(getErr)
	return wfRun
}

func testRunRuntime(tc client.Client) *TemporalRuntime {
	return &TemporalRuntime{temporalClient: tc}
}

// newTestRunHandle wires GetWorkflow → wfRun, then creates the handle (await starts immediately).
func newTestRunHandle(id, workflowID string, tc *temporalmocks.Client, wfRun *temporalmocks.WorkflowRun, cancel context.CancelFunc) *runHandle {
	tc.On("GetWorkflow", mock.Anything, workflowID, "").Return(wfRun).Maybe()
	return newRunHandle(id, workflowID, testRunRuntime(tc), cancel, nil)
}

func TestRunHandle_ID(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.Equal(t, "run-1", h.ID())
}

func TestRunHandle_Status_Running(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", "").
		Return(describeWorkflowRunning(), nil).Once()

	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)
}

func TestRunHandle_Status_NotFound(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-missing", "").
		Return(nil, errors.New("workflow not found for ID")).Once()

	wfRun := blockingWorkflowRun(release, nil, nil)
	h := newTestRunHandle("run-1", "wf-missing", tc, wfRun, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	_, err := h.Status(context.Background())
	require.ErrorIs(t, err, types.ErrRunNotFound)
}

func TestRunHandle_Status_NilRuntime(t *testing.T) {
	h := newRunHandle("run-1", "wf-1", nil, nil, nil)
	_, err := h.Status(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no runtime")
}

func TestRunHandle_Get_Success(t *testing.T) {
	release := make(chan struct{})
	want := &types.AgentRunResult{Content: "hello"} // RunID filled by await

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, want, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)

	close(release)

	got, err := h.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "hello", got.Content)
	require.Equal(t, "run-1", got.RunID)

	select {
	case <-h.Done():
	default:
		t.Fatal("Done should be closed after Get succeeds")
	}
}

func TestRunHandle_Get_Failure(t *testing.T) {
	release := make(chan struct{})
	wantErr := errors.New("workflow failed")

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, nil, wantErr)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)

	close(release)

	got, err := h.Get(context.Background())
	require.Nil(t, got)
	require.ErrorIs(t, err, wantErr)
}

func TestRunHandle_Get_UnblocksOnContextCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", "").
		Return(describeWorkflowRunning(), nil).Maybe()

	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "later"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := h.Get(ctx)
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Get did not unblock after context cancel")
	}

	// Workflow wait is still in progress; Status should still work.
	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)
}

func TestRunHandle_Get_WaitsForWorkflow(t *testing.T) {
	release := make(chan struct{})
	want := &types.AgentRunResult{Content: "later", RunID: "run-1"}

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, want, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := h.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, want, got)
	}()

	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Get did not return after workflow Get unblocked")
	}
}

// TestRunHandle_Get_RunContextTimeout verifies that when the run stops because its context
// deadline fired (WithTimeout or Run ctx), Get(Background) returns context.DeadlineExceeded
// rather than the raw Temporal "terminated" error string.
func TestRunHandle_Get_RunContextTimeout(t *testing.T) {
	release := make(chan struct{})
	termErr := errors.New("workflow terminated: agent run timeout")

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, nil, termErr)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)
	h.setStopCause(context.DeadlineExceeded)

	close(release)

	_, err := h.Get(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestRunHandle_Get_RunContextCanceled verifies that when the run stops because it was
// explicitly cancelled (Cancel() or Run ctx cancelled), Get(Background) returns
// context.Canceled rather than the raw Temporal "terminated" error string.
func TestRunHandle_Get_RunContextCanceled(t *testing.T) {
	release := make(chan struct{})
	termErr := errors.New("workflow terminated: run cancelled")

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, nil, termErr)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)
	h.setStopCause(context.Canceled)

	close(release)

	_, err := h.Get(context.Background())
	require.ErrorIs(t, err, context.Canceled)
}

func TestRunHandle_Cancel_Running(t *testing.T) {
	release := make(chan struct{})
	var cancelled atomic.Bool
	runCancel := func() { cancelled.Store(true) }

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, runCancel)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.NoError(t, h.Cancel(context.Background()))
	require.True(t, cancelled.Load())
}

func TestRunHandle_Cancel_NilCancel(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestRunHandle_Cancel_AfterDone(t *testing.T) {
	release := make(chan struct{})
	runCancel := func() {}

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "done"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, runCancel)

	close(release)
	_, err := h.Get(context.Background())
	require.NoError(t, err)

	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestRunHandle_Cancel_Twice(t *testing.T) {
	release := make(chan struct{})
	runCancel := func() {}

	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, runCancel)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.NoError(t, h.Cancel(context.Background()))
	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestRunHandle_Done_SameChannel(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	require.Equal(t, h.Done(), h.Done())
}

func TestRunHandle_Status_MapsCompleted(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeWorkflowExecution", mock.Anything, "wf-1", "").
		Return(&workflowservice.DescribeWorkflowExecutionResponse{
			WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
				Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
			},
		}, nil).Once()

	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)
	h := newTestRunHandle("run-1", "wf-1", tc, wfRun, nil)
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCompleted, st)
}

func TestMapWorkflowError_BudgetExceeded(t *testing.T) {
	raw := errors.New("workflow execution error: agent: per-run budget exceeded: total tokens 76 exceeds limit 50 (type: wrapError, retryable: true)")
	require.ErrorIs(t, mapWorkflowError(raw), types.ErrBudgetExceeded)
	require.ErrorIs(t, mapWorkflowError(types.ErrBudgetExceeded), types.ErrBudgetExceeded)

	unavail := errors.New("workflow execution error: agent: budget approval unavailable (stream not connected): no subscriber")
	require.ErrorIs(t, mapWorkflowError(unavail), types.ErrBudgetApprovalUnavailable)
	require.ErrorIs(t, mapWorkflowError(types.ErrBudgetApprovalUnavailable), types.ErrBudgetApprovalUnavailable)

	require.Equal(t, context.Canceled, mapWorkflowError(context.Canceled))
	require.Nil(t, mapWorkflowError(nil))
}

func TestRunHandle_Get_RemapsBudgetExceeded(t *testing.T) {
	release := make(chan struct{})
	tc := temporalmocks.NewClient(t)
	wfErr := errors.New("workflow execution error (type: , workflowID: wf-1): agent: per-run budget exceeded: total tokens 76 exceeds limit 50 (type: wrapError, retryable: true): agent: per-run budget exceeded")
	wfRun := blockingWorkflowRun(release, nil, wfErr)
	h := newTestRunHandle("run-budget", "wf-1", tc, wfRun, nil)

	close(release)
	waitHandleDone(t, h.Done())

	_, err := h.Get(context.Background())
	require.ErrorIs(t, err, types.ErrBudgetExceeded)
}
