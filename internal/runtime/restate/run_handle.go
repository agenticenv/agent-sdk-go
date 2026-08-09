package restate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	restateingress "github.com/restatedev/sdk-go/ingress"
)

var _ sdkruntime.RunHandle = (*runHandle)(nil)

// runHandle is the RestateRuntime implementation of [sdkruntime.RunHandle].
// Status, Get, and Cancel use Restate's invocationId rather than runID so that
// Output/Attach/Cancel by id work without knowing which handler (Run vs Stream) was used.
type runHandle struct {
	id           string // SDK run ID; also used as the ingress idempotency key
	invocationID string // Restate native invocationId from Send.Id() or POST /restate/lookup
	rt           *RestateRuntime
	doneCh       chan struct{}

	cancelOnce sync.Once
	mu         sync.Mutex
	cancelled  bool
	res        *types.AgentRunResult
	err        error
}

func newRunHandle(id, invocationID string, rt *RestateRuntime) *runHandle {
	h := &runHandle{
		id:           id,
		invocationID: invocationID,
		rt:           rt,
		doneCh:       make(chan struct{}),
	}
	go h.awaitCompletion()
	return h
}

func (h *runHandle) ID() string { return h.id }

// Status peeks at the invocation via Restate Output by invocationId.
// Returns [types.ErrRunNotFound] when Restate no longer holds the invocation.
func (h *runHandle) Status(ctx context.Context) (types.RunStatus, error) {
	if err := h.ensureReady(); err != nil {
		return "", err
	}

	h.mu.Lock()
	cancelled := h.cancelled
	h.mu.Unlock()
	if cancelled {
		return types.StatusCancelled, nil
	}

	_, err := withIngressRetry(ctx, h.rt, func(attemptCtx context.Context) (struct{}, error) {
		_, err := restateingress.InvocationById[*AgentLoopResponse](h.rt.ingressClient, h.invocationID).Output(attemptCtx)
		return struct{}{}, err
	})
	if err == nil {
		return types.StatusCompleted, nil
	}
	var notFound *restateingress.InvocationNotFoundError
	if errors.As(err, &notFound) {
		return "", types.ErrRunNotFound
	}
	var notReady *restateingress.InvocationNotReadyError
	if errors.As(err, &notReady) {
		return types.StatusRunning, nil
	}
	return types.StatusFailed, nil
}

// Cancel cancels the run via AgentLoop/Cancel (Restate CancelInvocation).
// Returns [types.ErrRunAlreadyCompleted] when Cancel has already been called.
func (h *runHandle) Cancel(ctx context.Context) error {
	if err := h.ensureReady(); err != nil {
		return err
	}
	var err error
	called := false
	h.cancelOnce.Do(func() {
		called = true
		err = h.rt.cancelInvocation(ctx, h.id, h.invocationID)
		if err == nil {
			h.mu.Lock()
			h.cancelled = true
			h.mu.Unlock()
		}
	})
	if !called {
		return types.ErrRunAlreadyCompleted
	}
	return err
}

// Get blocks until the invocation finishes and returns the result.
// Cancelling ctx unblocks Get but does not cancel the agent run; use Cancel for that.
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

func (h *runHandle) Done() <-chan struct{} { return h.doneCh }

func (h *runHandle) ensureReady() error {
	if h.rt == nil {
		return fmt.Errorf("restate: run handle %q has no runtime", h.id)
	}
	if strings.TrimSpace(h.invocationID) == "" {
		return fmt.Errorf("restate: run handle %q has no invocationId", h.id)
	}
	if h.rt.ingressClient == nil {
		return fmt.Errorf("restate: ingress client not configured")
	}
	return nil
}

// awaitCompletion Attach-es by invocationId and closes doneCh when finished.
func (h *runHandle) awaitCompletion() {
	defer close(h.doneCh)
	if h.rt == nil || strings.TrimSpace(h.invocationID) == "" || h.rt.ingressClient == nil {
		h.mu.Lock()
		h.err = fmt.Errorf("restate: run handle %q cannot await completion", h.id)
		h.mu.Unlock()
		return
	}

	out, err := restateingress.InvocationById[*AgentLoopResponse](
		h.rt.ingressClient, h.invocationID,
	).Attach(context.Background())

	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		if h.cancelled {
			h.err = context.Canceled
			return
		}
		h.err = err
		return
	}
	if out != nil && out.Result != nil {
		h.res = out.Result
		if h.res.RunID == "" {
			h.res.RunID = h.id
		}
	}
}
