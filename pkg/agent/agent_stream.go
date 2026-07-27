package agent

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

//go:generate mockgen -destination=./mocks/mock_agent_stream.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/agent AgentStream

// Streaming sentinel errors re-exported from internal/runtime so callers only import pkg/agent.
var (
	// ErrStreamNotFound is returned when a stream runID is not recognised by the runtime.
	ErrStreamNotFound = types.ErrStreamNotFound

	// ErrStreamOffsetNotSupported is returned when the runtime cannot replay at the requested offset
	// (e.g. LocalRuntime with fromOffset > 0).
	ErrStreamOffsetNotSupported = types.ErrStreamOffsetNotSupported

	// ErrStreamAlreadyConsumed is returned when [AgentStream.Events] is called again on a
	// handle that already handed out its in-process event channel (LocalRuntime).
	ErrStreamAlreadyConsumed = types.ErrStreamAlreadyConsumed

	// ErrRunAlreadyCompleted is returned by [Agent.GetAgentRun], [Agent.GetAgentStream],
	// [AgentStream.Events], or [AgentStream.Get] when the target run has already finished.
	// Callers should read conversation history from the memory store instead of reconnecting.
	ErrRunAlreadyCompleted = types.ErrRunAlreadyCompleted
)

// AgentStreamOption is a functional option applied to an [AgentStream.Events] call.
// Use [WithOffset] to resume a stream after a crash or reconnect.
type AgentStreamOption func(*agentStreamConfig)

// agentStreamConfig holds resolved options for a single [AgentStream.Events] call.
type agentStreamConfig struct {
	// fromOffset is the durable log offset to resume from (0 = from the beginning).
	fromOffset int64
}

// WithOffset instructs [AgentStream.Events] to resume the event stream starting at the
// given durable log offset. Use the last successfully processed offset saved before a crash.
// Offset 0 means "from the beginning" (first-time subscriber).
// Non-zero offsets are only supported by Temporal-backed agents; LocalRuntime returns
// [ErrStreamOffsetNotSupported] for offset > 0.
func WithOffset(offset int64) AgentStreamOption {
	return func(cfg *agentStreamConfig) {
		cfg.fromOffset = offset
	}
}

// AgentStream is the handle returned by [Agent.Stream] and [Agent.GetAgentStream].
//
// First-time subscriber pattern:
//
//	agentStream, err := agent.Stream(ctx, input, opts) // cancel ctx → cancel run
//	ch, err := agentStream.Events(eventsCtx)           // cancel eventsCtx → subscriber only
//	for event := range ch { ... }
//
// Wait for final result (same as [AgentRun], without consuming Events):
//
//	agentStream, err := agent.Stream(ctx, input, opts)
//	result, err := agentStream.Get(ctx)
//
// Non-blocking wait, then Get:
//
//	<-agentStream.Done()
//	result, err := agentStream.Get(ctx)
//
// Reconnect pattern (resume after crash while the run is still live):
//
//	agentStream, err := agent.GetAgentStream(ctx, savedRunID)
//	// err is ErrRunAlreadyCompleted if the run already finished — use conversation/memory instead.
//	ch, err := agentStream.Events(ctx, agent.WithOffset(savedOffset))
//	for event := range ch { ... }
type AgentStream interface {
	// ID returns the unique run identifier for this stream, stable across process restarts.
	ID() string

	// Status returns the current lifecycle status of the underlying run.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Status(ctx context.Context) (RunStatus, error)

	// Cancel requests cancellation of the underlying agent run.
	// Returns [ErrRunAlreadyCompleted] when the run has already finished.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Cancel(ctx context.Context) error

	// Get blocks until the stream run finishes and returns the result pointer.
	// Cancelling ctx unblocks Get but does NOT cancel the agent run itself —
	// use [AgentStream.Cancel] for that.
	Get(ctx context.Context) (*AgentRunResult, error)

	// Done returns a channel that is closed when the stream run finishes (success or failure).
	// Safe to call multiple times; always returns the same channel.
	Done() <-chan struct{}

	// Approve completes a tool or delegation approval using the token from the CUSTOM approval
	// event and the chosen status ([ApprovalStatusApproved] or [ApprovalStatusRejected]).
	// Returns [ErrApprovalAlreadyResolved] when the token was already completed.
	Approve(ctx context.Context, approvalToken string, status ApprovalStatus) error

	// Events subscribes to the agent event stream and returns a receive-only channel.
	// The channel is closed after the terminal lifecycle event (RUN_FINISHED / RUN_ERROR),
	// or when ctx is cancelled (Temporal: stops this subscriber only; the agent run continues).
	// Cancelling [Agent.Stream]'s ctx cancels the run — use a separate Events ctx for reconnect.
	//
	// opts may include [WithOffset] to resume from a saved position after a crash.
	// Omitting WithOffset (or passing offset 0) starts from the beginning of the run.
	//
	// Returns [ErrRunAlreadyCompleted] if the run has already finished.
	// Returns [ErrStreamOffsetNotSupported] when the runtime does not support the requested offset.
	Events(ctx context.Context, opts ...AgentStreamOption) (<-chan AgentEvent, error)
}

// agentStream is the [AgentStream] implementation: a thin wrapper over [runtime.StreamHandle].
//
// Lifecycle: when streams is non-nil, [newAgentStream] inserts this handle into Agent.streams
// and starts [agentStream.awaitCompletion], which removes it via [store.KV.DeleteIf] after Done.
// Events may be called more than once with different offsets; each call opens a fresh
// subscription on the runtime handle.
type agentStream struct {
	sh runtime.StreamHandle
}

// newAgentStream wraps sh. sh must be non-nil.
// When streams is non-nil, registers under [runtime.StreamHandle.ID] and starts a waiter that
// unregisters on Done. When streams is nil, returns an untracked handle (tests only).
func newAgentStream(sh runtime.StreamHandle, streams *store.KV[string, *agentStream]) *agentStream {
	s := &agentStream{sh: sh}
	if streams != nil {
		streams.Set(sh.ID(), s)
		go s.awaitCompletion(streams)
	}
	return s
}

// awaitCompletion waits for [runtime.StreamHandle.Done], then removes this handle from streams
// only if the map still points at this instance ([store.KV.DeleteIf]).
func (s *agentStream) awaitCompletion(streams *store.KV[string, *agentStream]) {
	if s.sh == nil || streams == nil {
		return
	}
	<-s.sh.Done()
	streams.DeleteIf(s.sh.ID(), s)
}

func (s *agentStream) ID() string {
	if s.sh == nil {
		return ""
	}
	return s.sh.ID()
}

// Status delegates to [runtime.StreamHandle.Status] (read-only; does not touch Agent.streams).
func (s *agentStream) Status(ctx context.Context) (RunStatus, error) {
	if s.sh == nil {
		return "", ErrRunNotFound
	}
	return s.sh.Status(ctx)
}

// Cancel delegates to [runtime.StreamHandle.Cancel].
func (s *agentStream) Cancel(ctx context.Context) error {
	if s.sh == nil {
		return ErrRunNotFound
	}
	return s.sh.Cancel(ctx)
}

// Get delegates to [runtime.StreamHandle.Get].
func (s *agentStream) Get(ctx context.Context) (*AgentRunResult, error) {
	if s.sh == nil {
		return nil, ErrRunNotFound
	}
	return s.sh.Get(ctx)
}

// Done delegates to [runtime.StreamHandle.Done].
func (s *agentStream) Done() <-chan struct{} {
	if s.sh == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.sh.Done()
}

// Approve delegates to [runtime.StreamHandle.Approve].
func (s *agentStream) Approve(ctx context.Context, approvalToken string, status ApprovalStatus) error {
	if s.sh == nil {
		return ErrRunNotFound
	}
	return s.sh.Approve(ctx, approvalToken, status)
}

// Events resolves opts into an offset, then delegates to [runtime.StreamHandle.Events].
func (s *agentStream) Events(ctx context.Context, opts ...AgentStreamOption) (<-chan AgentEvent, error) {
	if s.sh == nil {
		return nil, ErrRunNotFound
	}
	cfg := &agentStreamConfig{fromOffset: 0}
	for _, opt := range opts {
		opt(cfg)
	}
	ch, err := s.sh.Events(ctx, cfg.fromOffset)
	if err != nil {
		return nil, err
	}
	return ch, nil
}
