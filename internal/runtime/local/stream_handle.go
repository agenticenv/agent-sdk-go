package local

import (
	"context"
	"sync"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

var _ sdkruntime.StreamHandle = (*streamHandle)(nil)

// streamHandle is the LocalRuntime implementation of [sdkruntime.StreamHandle].
//
// It embeds [runHandle] for ID/Status/Cancel/Get/Done. The event channel lives on
// the stream handle. In-process reconnect/lookup is managed at the agent layer,
// not via runtime maps.
//
// Lifecycle: LocalRuntime.Stream creates the handle with a cancel func and event
// channel, starts the agent loop in a goroutine, and that goroutine calls
// [runHandle.markDone] when finished. [streamHandle.Events] hands out the channel
// once (LocalRuntime has no durable replay).
type streamHandle struct {
	*runHandle
	eventsOnce sync.Once
	eventCh    <-chan events.AgentEvent
}

// newStreamHandle creates a live stream handle for runID, embedding a new [runHandle].
// cancel aborts the run context; eventCh is the receive-only event stream
// (subscribe-before-start), handed out once by [streamHandle.Events].
func newStreamHandle(id string, cancel context.CancelFunc, eventCh <-chan events.AgentEvent) *streamHandle {
	return &streamHandle{
		runHandle: newRunHandle(id, cancel),
		eventCh:   eventCh,
	}
}

// Events returns this run's in-process event channel (subscribe-before-start).
// fromOffset must be 0; non-zero offsets return [types.ErrStreamOffsetNotSupported].
// Returns [types.ErrRunAlreadyCompleted] if the run has already finished (no channel).
// The channel is handed out once; a second call while the run is still live returns
// [types.ErrStreamAlreadyConsumed].
func (h *streamHandle) Events(_ context.Context, fromOffset int64) (<-chan events.AgentEvent, error) {
	if fromOffset > 0 {
		return nil, types.ErrStreamOffsetNotSupported
	}

	h.mu.Lock()
	terminal := h.status.IsTerminal()
	h.mu.Unlock()
	if terminal {
		return nil, types.ErrRunAlreadyCompleted
	}

	var ch <-chan events.AgentEvent
	h.eventsOnce.Do(func() {
		ch = h.eventCh
		h.eventCh = nil // ownership transferred to caller
	})
	if ch == nil {
		return nil, types.ErrStreamAlreadyConsumed
	}
	return ch, nil
}
