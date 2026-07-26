package temporal

import (
	"context"
	"fmt"
	"sync"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

var _ sdkruntime.RunHandle = (*runHandle)(nil)

// runHandle is the TemporalRuntime implementation of [sdkruntime.RunHandle].
//
// Lifecycle methods talk to Temporal for this run's workflowID only — callers never
// pass a runID back into TemporalRuntime for Status/Cancel/Get.
//
// Lifecycle: TemporalRuntime.Run and TemporalRuntime.GetRunHandle both create the
// handle with a real run-context cancel and activeRuns cleanup. A background
// goroutine waits via getRunResult and closes Done; [runHandle.Get] waits on that
// signal. Cancel invokes the run context cancel; driveRun then stopWorkflow.
// Same-process reconnect reuses the existing activeRuns entry (and its cancel).
// Crash reconnect that builds a new handle also gets a fresh cancel, so Cancel
// works after recovery. Known gap (see TODOs on GetRunHandle): reconnect does not
// yet reject a fingerprint/config mismatch after redeploy.
type runHandle struct {
	id         string
	workflowID string
	rt         *TemporalRuntime

	doneCh chan struct{}

	cancelOnce sync.Once
	cancel     context.CancelFunc

	cleanupOnce sync.Once
	cleanup     func()

	mu  sync.Mutex
	res *types.AgentRunResult
	err error
}

// newRunHandle creates a handle for runID / workflowID.
// cancel is the run context cancel from Run/Stream/GetRunHandle/GetStreamHandle
// (nil only in tests). cleanup runs once from awaitCompletion after the result is
// stored (e.g. activeRuns/activeStreams Delete). Starts a background waiter so
// Done closes when the workflow finishes.
func newRunHandle(id, workflowID string, rt *TemporalRuntime, cancel context.CancelFunc, cleanup func()) *runHandle {
	h := &runHandle{
		id:         id,
		workflowID: workflowID,
		rt:         rt,
		doneCh:     make(chan struct{}),
		cancel:     cancel,
		cleanup:    cleanup,
	}
	go h.awaitCompletion()
	return h
}

func (h *runHandle) ID() string { return h.id }

// Status describes this run's Temporal workflow execution.
// Returns [types.ErrRunNotFound] when the workflow is unknown.
func (h *runHandle) Status(ctx context.Context) (types.RunStatus, error) {
	if h.rt == nil {
		return "", fmt.Errorf("temporal: run handle %q has no runtime", h.id)
	}
	return h.rt.getRunStatus(ctx, h.workflowID)
}

// Cancel cancels the run context so driveRun can stopWorkflow.
// Returns [types.ErrRunAlreadyCompleted] when cancelOnce already ran
// (prior Cancel or [runHandle.awaitCompletion]) or when cancel was nil.
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

// Get blocks until the run finishes and returns the stored result.
// Cancelling ctx unblocks Get but does not cancel the agent run —
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

// Done returns the channel closed when the workflow finishes.
func (h *runHandle) Done() <-chan struct{} { return h.doneCh }

// awaitCompletion waits for the Temporal workflow result and closes Done.
// Uses context.Background so a cancelled Get caller does not abort the wait.
// Burns cancelOnce (clears cancel without calling it) so later Cancel is already-completed,
// without cancelling runCtx again and racing driveRun into stopWorkflow.
// Invokes cleanup once after storing the result (before Done closes).
func (h *runHandle) awaitCompletion() {
	defer close(h.doneCh)
	defer func() {
		h.cancelOnce.Do(func() {
			h.cancel = nil
		})
	}()

	if h.rt == nil {
		h.mu.Lock()
		h.err = fmt.Errorf("temporal: run handle %q has no runtime", h.id)
		h.mu.Unlock()
		return
	}

	result, err := h.rt.getRunResult(context.Background(), h.workflowID)
	if result != nil && result.RunID == "" {
		result.RunID = h.id
	}
	h.mu.Lock()
	h.err = err
	h.res = result
	h.mu.Unlock()

	h.cleanupOnce.Do(func() {
		if h.cleanup != nil {
			h.cleanup()
			h.cleanup = nil
		}
	})
}
