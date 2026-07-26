package local

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRunHandle_ID(t *testing.T) {
	h := newRunHandle("run-1", func() {})
	require.Equal(t, "run-1", h.ID())
}

func TestRunHandle_Status_InitiallyRunning(t *testing.T) {
	h := newRunHandle("run-1", func() {})
	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)
}

func TestRunHandle_Complete_Success(t *testing.T) {
	h := newRunHandle("run-1", func() {})
	want := &types.AgentRunResult{Content: "ok", RunID: "run-1"}

	h.markDone(want, nil)

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCompleted, st)

	got, err := h.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, got)

	select {
	case <-h.Done():
	default:
		t.Fatal("Done should be closed after complete")
	}
}

func TestRunHandle_Complete_Failure(t *testing.T) {
	h := newRunHandle("run-1", func() {})
	wantErr := errors.New("loop failed")

	h.markDone(nil, wantErr)

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusFailed, st)

	got, err := h.Get(context.Background())
	require.Nil(t, got)
	require.ErrorIs(t, err, wantErr)
}

func TestRunHandle_Complete_ContextCanceledSetsCancelled(t *testing.T) {
	h := newRunHandle("run-1", func() {})

	h.markDone(nil, context.Canceled)

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCancelled, st)

	got, err := h.Get(context.Background())
	require.Nil(t, got)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRunHandle_Complete_DeadlineExceededSetsFailed(t *testing.T) {
	h := newRunHandle("run-1", func() {})

	h.markDone(nil, context.DeadlineExceeded)

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusFailed, st)
}

func TestRunHandle_Cancel_InvokesCancelFunc(t *testing.T) {
	cancelled := false
	h := newRunHandle("run-1", func() { cancelled = true })

	require.NoError(t, h.Cancel(context.Background()))
	require.True(t, cancelled)

	// Status stays Running until markDone; Cancel only cancels the context.
	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)

	// Done stays open until markDone (loop exit).
	select {
	case <-h.Done():
		t.Fatal("Done should stay open after Cancel until markDone")
	default:
	}

	// Second Cancel is unsupported (cancel already consumed).
	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)

	h.markDone(nil, context.Canceled)
	select {
	case <-h.Done():
	default:
		t.Fatal("Done should close after markDone")
	}

	st, err = h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCancelled, st)
}

func TestRunHandle_Cancel_NilCancelFunc(t *testing.T) {
	h := newRunHandle("run-1", nil)
	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestRunHandle_Cancel_AfterComplete(t *testing.T) {
	h := newRunHandle("run-1", func() {})
	h.markDone(&types.AgentRunResult{Content: "done"}, nil)

	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestRunHandle_Get_UnblocksOnContextCancel(t *testing.T) {
	h := newRunHandle("run-1", func() {})
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

	// Run itself is still live.
	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)
}

func TestRunHandle_Get_WaitsForComplete(t *testing.T) {
	h := newRunHandle("run-1", func() {})
	want := &types.AgentRunResult{Content: "later"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := h.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, want, got)
	}()

	time.Sleep(20 * time.Millisecond)
	h.markDone(want, nil)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Get did not return after complete")
	}
}

func TestRunHandle_Done_SameChannel(t *testing.T) {
	h := newRunHandle("run-1", func() {})
	require.Equal(t, h.Done(), h.Done())
}
