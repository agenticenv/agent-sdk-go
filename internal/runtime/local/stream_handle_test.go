package local

import (
	"context"
	"errors"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStreamHandle_ID(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)
	require.Equal(t, "run-1", h.ID())
}

func TestStreamHandle_Status_InitiallyRunning(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)
}

func TestStreamHandle_Complete_Success(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)

	h.markDone(nil, nil)

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCompleted, st)

	select {
	case <-h.doneCh:
	default:
		t.Fatal("doneCh should be closed after complete")
	}
}

func TestStreamHandle_Complete_Failure(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)

	h.markDone(nil, errors.New("loop failed"))

	st, err := h.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusFailed, st)
}

func TestStreamHandle_Cancel_InvokesCancelFunc(t *testing.T) {
	cancelled := false
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() { cancelled = true }, ch)

	require.NoError(t, h.Cancel(context.Background()))
	require.True(t, cancelled)
	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestStreamHandle_Cancel_NilCancelFunc(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, nil, ch)
	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestStreamHandle_Cancel_AfterComplete(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)
	h.markDone(nil, nil)

	require.ErrorIs(t, h.Cancel(context.Background()), types.ErrRunAlreadyCompleted)
}

func TestStreamHandle_Events_ReturnsChannelOnce(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)

	got, err := h.Events(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, (<-chan events.AgentEvent)(ch), got)

	// Run still live — second Events is "already consumed", not "completed".
	_, err = h.Events(context.Background(), 0)
	require.ErrorIs(t, err, types.ErrStreamAlreadyConsumed)
}

func TestStreamHandle_Events_SecondCallAfterComplete(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)

	_, err := h.Events(context.Background(), 0)
	require.NoError(t, err)

	h.markDone(nil, nil)

	_, err = h.Events(context.Background(), 0)
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
}

func TestStreamHandle_Events_OffsetUnsupported(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)

	_, err := h.Events(context.Background(), 5)
	require.ErrorIs(t, err, types.ErrStreamOffsetNotSupported)
}

func TestStreamHandle_Events_AfterComplete(t *testing.T) {
	ch := make(chan events.AgentEvent)
	h := newStreamHandle("run-1", nil, func() {}, ch)
	h.markDone(nil, nil)

	_, err := h.Events(context.Background(), 0)
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
}
