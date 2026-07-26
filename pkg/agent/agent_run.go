package agent

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

//go:generate mockgen -destination=./mocks/mock_agent_run.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/agent AgentRun

// RunStatus is the lifecycle status of an agent run. Re-exported from internal/types so callers
// only import pkg/agent.
type RunStatus = types.RunStatus

// Run status constants, mirroring [types.RunStatus] values for use without a types import.
const (
	StatusPending   = types.StatusPending
	StatusRunning   = types.StatusRunning
	StatusCompleted = types.StatusCompleted
	StatusFailed    = types.StatusFailed
	StatusCancelled = types.StatusCancelled
)

// Sentinel errors re-exported from internal/runtime so callers only import pkg/agent.
// ErrRunAlreadyCompleted is declared in agent_stream.go (shared by run and stream APIs).
var (
	// ErrRunNotFound is returned when a runID is not recognised by the runtime.
	ErrRunNotFound = types.ErrRunNotFound
)

// AgentRun is the handle returned by [Agent.Run] and [Agent.GetAgentRun].
//
// Blocking pattern — call Get directly; it blocks until the run finishes:
//
//	agentRun, err := agent.Run(ctx, input, opts)
//	result, err := agentRun.Get(ctx)
//
// Non-blocking pattern — wait on Done, then call Get:
//
//	agentRun, err := agent.Run(ctx, input, opts)
//	<-agentRun.Done()
//	result, err := agentRun.Get(ctx)
//
// Reconnect pattern — resume a still-running run from a previous process:
//
//	agentRun, err := agent.GetAgentRun(ctx, savedRunID)
//	// err is ErrRunAlreadyCompleted if the run already finished — use conversation/memory instead.
//	result, err := agentRun.Get(ctx)
type AgentRun interface {
	// ID returns the unique run identifier, stable across process restarts.
	ID() string

	// Status returns the current lifecycle status of the run.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Status(ctx context.Context) (RunStatus, error)

	// Cancel requests cancellation of the run.
	// Returns [ErrRunAlreadyCompleted] when the run has already finished.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Cancel(ctx context.Context) error

	// Get blocks until the run finishes and returns the result pointer.
	// Cancelling ctx unblocks Get but does NOT cancel the agent run itself —
	// use [AgentRun.Cancel] for that.
	Get(ctx context.Context) (*AgentRunResult, error)

	// Done returns a channel that is closed when the run finishes (success or failure).
	// Safe to call multiple times; always returns the same channel.
	Done() <-chan struct{}
}

// agentRun is the [AgentRun] implementation: a thin wrapper over [runtime.RunHandle].
//
// Lifecycle: when runs is non-nil, [newAgentRun] inserts this handle into Agent.runs and
// starts [agentRun.awaitCompletion], which removes it via [store.KV.DeleteIf] after Done.
type agentRun struct {
	rh runtime.RunHandle
}

// newAgentRun wraps rh. rh must be non-nil.
// When runs is non-nil, registers under [runtime.RunHandle.ID] and starts a waiter that
// unregisters on Done. When runs is nil, returns an untracked handle (tests only).
func newAgentRun(rh runtime.RunHandle, runs *store.KV[string, *agentRun]) *agentRun {
	r := &agentRun{rh: rh}
	if runs != nil {
		runs.Set(rh.ID(), r)
		go r.awaitCompletion(runs)
	}
	return r
}

// awaitCompletion waits for [runtime.RunHandle.Done], then removes this handle from runs
// only if the map still points at this instance ([store.KV.DeleteIf]).
func (r *agentRun) awaitCompletion(runs *store.KV[string, *agentRun]) {
	if r.rh == nil {
		return
	}
	<-r.rh.Done()
	runs.DeleteIf(r.rh.ID(), r)
}

func (r *agentRun) ID() string {
	if r.rh == nil {
		return ""
	}
	return r.rh.ID()
}

// Status delegates to [runtime.RunHandle.Status].
func (r *agentRun) Status(ctx context.Context) (RunStatus, error) {
	if r.rh == nil {
		return "", ErrRunNotFound
	}
	return r.rh.Status(ctx)
}

// Cancel delegates to [runtime.RunHandle.Cancel].
func (r *agentRun) Cancel(ctx context.Context) error {
	if r.rh == nil {
		return ErrRunNotFound
	}
	return r.rh.Cancel(ctx)
}

// Get delegates to [runtime.RunHandle.Get].
func (r *agentRun) Get(ctx context.Context) (*AgentRunResult, error) {
	if r.rh == nil {
		return nil, ErrRunNotFound
	}
	return r.rh.Get(ctx)
}

// Done delegates to [runtime.RunHandle.Done].
func (r *agentRun) Done() <-chan struct{} {
	if r.rh == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return r.rh.Done()
}
