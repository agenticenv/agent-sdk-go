package local

import (
	"context"
	"errors"
	"sync"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	durable "github.com/agenticenv/durable-go"
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
	rt     *LocalRuntime
	doneCh chan struct{}

	cancelOnce sync.Once
	doneOnce   sync.Once
	cancel     context.CancelFunc

	mu     sync.Mutex
	status types.RunStatus
	res    *types.AgentRunResult
	err    error

	// durable-go wiring, set via setDurable when this handle's run is durable-go-backed.
	// nil (the default) means Cancel/markDone use the original plain-ctx-cancel behavior.
	durableEngine *durable.Engine
	durableTaskID string
}

// setDurable marks this handle as backing a durable-go run, so Cancel routes through
// engine.CancelRun instead of (or in addition to) the plain ctx cancel func.
func (h *runHandle) setDurable(engine *durable.Engine, taskID string) {
	h.durableEngine = engine
	h.durableTaskID = taskID
}

// newRunHandle creates a live handle for runID. cancel aborts the run context;
// pass a non-nil cancel from context.WithCancel (or WithTimeout).
func newRunHandle(id string, rt *LocalRuntime, cancel context.CancelFunc) *runHandle {
	return &runHandle{
		id:     id,
		rt:     rt,
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

// Cancel requests cancellation of the run. Durable handles ([setDurable]) call
// [durable.Engine.CancelRun] — durable-go persists a cancel signal and, if this run is
// executing in this process, cancels its Task.Exec ctx immediately; a resumed run
// checks the signal before Task.Exec runs and fails fast on its next RunStep. This is
// cooperative only: a step function that never checks ctx keeps running in the
// background regardless (see durable-go CancelRun doc). It does not update status or
// close Done directly — the agent loop must actually exit and [runHandle.markDone]
// owns that (ErrRunCancelled / context.Canceled → Cancelled).
//
// Non-durable handles keep the original plain-ctx-cancel behavior.
//
// Eventual consistency note (durable only): this also cancels the plain local run ctx,
// which can make [runHandle.Get] return immediately with context.Canceled before
// durable-go's own background goroutine finishes persisting the run's terminal
// meta.json (durable.Engine.CancelRun's doc: "RunTask.Get... return[s] promptly
// regardless" of whether that goroutine has exited). A [LocalRuntime.GetRunHandle] or
// [durable.Engine.GetTask] call made immediately after Get returns may briefly still
// observe StatusRunning; poll if you need to observe the settled on-disk state.
//
// Returns [types.ErrRunAlreadyCompleted] when the run is already terminal (durable:
// engine.CancelRun returned durable.ErrRunAlreadyFinished; non-durable: cancelOnce
// already ran via a prior Cancel or markDone, or cancel was nil).
func (h *runHandle) Cancel(ctx context.Context) error {
	if h.durableEngine != nil {
		err := h.durableEngine.CancelRun(ctx, h.durableTaskID, h.id)
		h.cancelOnce.Do(func() {
			if h.cancel != nil {
				h.cancel()
				h.cancel = nil
			}
		})
		if err != nil {
			if errors.Is(err, durable.ErrRunAlreadyFinished) {
				return types.ErrRunAlreadyCompleted
			}
			return err
		}
		return nil
	}

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
//   - durable.ErrRunCancelled (via engine.CancelRun) → Cancelled. Matched both by
//     errors.Is (a live, in-process run's error is the real sentinel) and by string
//     (err.Error() == durable.ErrRunCancelled.Error()) because a run reloaded from disk
//     reconstructs its error via errors.New(info.Error) — a different value that
//     errors.Is cannot match. See durable-go's ErrRunCancelled doc.
//   - other err → Failed (including context.DeadlineExceeded / timeouts)
func (h *runHandle) markDone(res *types.AgentRunResult, err error) {
	h.mu.Lock()
	h.res = res
	h.err = err
	switch {
	case err == nil:
		h.status = types.StatusCompleted
	case errors.Is(err, context.Canceled),
		errors.Is(err, durable.ErrRunCancelled),
		err.Error() == durable.ErrRunCancelled.Error():
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
