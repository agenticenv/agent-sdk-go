package local

import (
	"context"
	"errors"
	"sync"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

var _ sdkruntime.RunHandle = (*runHandle)(nil)

// runHandle is the LocalRuntime implementation of [sdkruntime.RunHandle].
//
// It is self-contained: status, cancel, result, and Done all live on the handle.
// In-process reconnect/lookup is managed at the agent layer, not via runtime maps.
//
// Lifecycle: the caller (LocalRuntime.Run) creates the handle with a cancel func,
// starts the agent loop in a goroutine, and that goroutine calls [runHandle.markDone]
// when finished. Cancel only cancels the run context; markDone closes Done.
type runHandle struct {
	id     string
	doneCh chan struct{}

	cancelOnce sync.Once
	doneOnce   sync.Once
	cancel     context.CancelFunc

	mu     sync.Mutex
	status types.RunStatus
	res    *types.AgentRunResult
	err    error
}

// newRunHandle creates a live handle for runID. cancel aborts the run context;
// pass a non-nil cancel from context.WithCancel (or WithTimeout).
func newRunHandle(id string, cancel context.CancelFunc) *runHandle {
	return &runHandle{
		id:     id,
		doneCh: make(chan struct{}),
		cancel: cancel,
		status: types.StatusRunning,
	}
}

func (h *runHandle) ID() string { return h.id }

// Status returns the handle's current lifecycle status.
func (h *runHandle) Status(_ context.Context) (types.RunStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status, nil
}

// Cancel requests cancellation of the run context only. It does not update status
// or close Done — the agent loop must exit and [runHandle.markDone] owns that
// (context.Canceled → Cancelled).
// Returns [types.ErrRunAlreadyCompleted] when cancelOnce already ran
// (prior Cancel or [runHandle.markDone]) or when cancel was nil.
func (h *runHandle) Cancel(_ context.Context) error {
	cancelled := false

	h.cancelOnce.Do(func() {
		if h.cancel != nil {
			h.cancel()
			h.cancel = nil
			cancelled = true
		}
	})

	if !cancelled {
		return types.ErrRunAlreadyCompleted
	}
	return nil
}

// Get blocks until [runHandle.markDone] closes Done and returns the stored
// result/error. Cancelling ctx unblocks Get but does not cancel the agent run —
// use [runHandle.Cancel].
func (h *runHandle) Get(ctx context.Context) (*types.AgentRunResult, error) {
	select {
	case <-h.doneCh:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.res, h.err
	default:
	}

	select {
	case <-h.doneCh:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.res, h.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Done returns the channel closed by [runHandle.markDone] when the run finishes.
func (h *runHandle) Done() <-chan struct{} { return h.doneCh }

// markDone stores the terminal result, sets status, releases the run context if
// still live, and closes Done. Call from the run goroutine when the agent loop
// finishes. markDone is the only place that writes terminal status:
//   - err == nil → Completed
//   - errors.Is(err, context.Canceled) → Cancelled (parent ctx cancel or Cancel)
//   - other err → Failed (including context.DeadlineExceeded / timeouts)
func (h *runHandle) markDone(res *types.AgentRunResult, err error) {
	h.mu.Lock()
	h.res = res
	h.err = err
	switch {
	case err == nil:
		h.status = types.StatusCompleted
	case errors.Is(err, context.Canceled):
		h.status = types.StatusCancelled
	default:
		h.status = types.StatusFailed
	}
	h.mu.Unlock()

	h.cancelOnce.Do(func() {
		if h.cancel != nil {
			h.cancel()
			h.cancel = nil
		}
	})
	h.doneOnce.Do(func() {
		close(h.doneCh)
	})
}
