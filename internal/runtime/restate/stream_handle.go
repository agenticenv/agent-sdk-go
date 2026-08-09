package restate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	restateingress "github.com/restatedev/sdk-go/ingress"
)

var _ sdkruntime.StreamHandle = (*streamHandle)(nil)

const (
	// After the run finishes, keep peeking until this many consecutive empty responses
	// to drain late publishes before emitting the synthetic RUN_FINISHED event.
	pubsubDrainEmptyPeeks = 3
	pubsubDrainInterval   = 50 * time.Millisecond
)

// streamHandle is the RestateRuntime implementation of [sdkruntime.StreamHandle].
// Embeds [runHandle] for ID / Status / Cancel / Get / Done.
// Events are pulled from the AgentEventLog PubSub Virtual Object (key = runID).
type streamHandle struct {
	*runHandle
}

func newStreamHandle(id, invocationID string, rt *RestateRuntime) *streamHandle {
	return &streamHandle{runHandle: newRunHandle(id, invocationID, rt)}
}

// Approve resolves a pending tool approval by id (approvalToken = awakeable id).
// Symmetric with Temporal CompleteActivity via the SDK approval token pattern.
func (h *streamHandle) Approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	if h.rt == nil {
		return fmt.Errorf("restate: stream handle %q is not configured", h.id)
	}
	return h.rt.approve(ctx, approvalToken, status)
}

// Events subscribes to this run's AgentEventLog PubSub topic starting at fromOffset.
// fromOffset == 0 emits synthetic RUN_STARTED then reads from offset 0;
// fromOffset > 0 resumes after reconnect without re-emitting RUN_STARTED.
// Cancelling ctx unsubscribes only; it does not cancel the agent run.
// The channel closes after the run completes and all events are drained.
func (h *streamHandle) Events(ctx context.Context, fromOffset int64) (<-chan events.AgentEvent, error) {
	if h.rt == nil || h.rt.ingressClient == nil {
		return nil, fmt.Errorf("restate: stream handle %q is not configured for Events", h.id)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fromOffset < 0 {
		return nil, fmt.Errorf("restate: fromOffset must be >= 0")
	}

	// Bounded status gate so a short Events ctx deadline does not abort the check.
	describeCtx, describeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer describeCancel()
	status, err := h.Status(describeCtx)
	if err != nil {
		return nil, err
	}
	if status.IsTerminal() {
		return nil, types.ErrRunAlreadyCompleted
	}

	outCh := make(chan events.AgentEvent, 64)
	go h.deliverEvents(ctx, fromOffset, outCh)
	return outCh, nil
}

// deliverEvents pulls from AgentEventLog/Pull (Wait=false) while the run is active,
// drains until quiet after run completion, then emits a synthetic RUN_FINISHED or RUN_ERROR.
// The channel is closed before deliverEvents returns.
func (h *streamHandle) deliverEvents(ctx context.Context, fromOffset int64, outCh chan<- events.AgentEvent) {
	defer close(outCh)

	if fromOffset == 0 {
		outCh <- events.NewAgentRunStartedEvent("", h.id)
	}
	rootName := strings.TrimSpace(h.rt.AgentSpec.Name)

	// runPullLoop returns false when the caller cancelled before the run finished — no terminal event.
	if !h.runPullLoop(ctx, fromOffset, outCh) {
		return
	}

	result, err := h.Get(context.Background())
	if err != nil {
		outCh <- events.NewAgentRunErrorEvent(terminalStreamErrorMessage(err))
		return
	}
	outCh <- syntheticStreamCompleteEvent(result, "", h.id, rootName)
}

// runPullLoop polls AgentEventLog/Pull in a loop until the run finishes and all queued
// events are drained. Returns true when the run completed and a terminal event should be
// emitted. Returns false when the caller context was cancelled before the run finished.
func (h *streamHandle) runPullLoop(ctx context.Context, fromOffset int64, outCh chan<- events.AgentEvent) bool {
	// runDoneCh is closed by a background goroutine once the Restate invocation finishes.
	// The goroutine exits cleanly when ctx is cancelled (no goroutine leak).
	runDoneCh := make(chan struct{})
	go func() {
		select {
		case <-h.Done():
			close(runDoneCh)
		case <-ctx.Done():
		}
	}()

	offset := fromOffset
	emptyAfterDone := 0

	for {
		runDone := channelReady(runDoneCh)

		// Caller cancelled while the run is still active — unsubscribe without a terminal event.
		if ctx.Err() != nil && !runDone {
			return false
		}

		resp, err := h.pullOnce(ctx, runDone, offset)
		if err != nil {
			// Context cancellation during an active run is a clean unsubscribe, not an error.
			if ctx.Err() != nil && !runDone {
				return false
			}
			// Topic cleared after the post-completion Clear (EventLog.TTL) — stop Pulling and finish.
			if isAgentEventLogCleared(err) {
				return true
			}
			if !runDone {
				h.rt.logger.Warn(ctx, "stream: pubsub pull error, falling back to terminal result",
					slog.String("scope", "runtime"),
					slog.String("runID", h.id),
					slog.Any("error", err))
			}
			return true
		}

		// Advance offset regardless of whether there are messages.
		offset = resp.NextOffset

		if len(resp.Messages) == 0 {
			if runDone {
				// Run finished; count consecutive empty peeks until the stream is quiet.
				emptyAfterDone++
				if emptyAfterDone >= pubsubDrainEmptyPeeks {
					return true
				}
				select {
				case <-ctx.Done():
					return true // caller left during drain — still emit terminal
				case <-time.After(pubsubDrainInterval):
				}
			} else {
				// Run still active; wait for a new message, run completion, or cancellation.
				select {
				case <-runDoneCh:
				case <-ctx.Done():
					return false
				case <-time.After(pubsubPullInterval):
				}
			}
			continue
		}

		emptyAfterDone = 0
		// Messages in this Pull batch start at the request offset (absolute PubSub index).
		msgOffset := offset
		for _, raw := range resp.Messages {
			ev, decErr := events.EventFromJSON(raw)
			if decErr != nil {
				h.rt.logger.Warn(ctx, "stream: skipping undecoded pubsub message",
					slog.String("scope", "runtime"),
					slog.String("runID", h.id),
					slog.Any("error", decErr))
				msgOffset++
				continue
			}
			if ev == nil {
				msgOffset++
				continue
			}
			// Attach PubSub offset so callers can resume with Events(WithOffset) after reconnect.
			if os, ok := ev.(offsetSetter); ok {
				os.SetOffset(msgOffset)
			}
			msgOffset++
			select {
			case outCh <- ev:
			case <-ctx.Done():
				if !runDone {
					return false // caller cancelled — skip remaining events
				}
				// Run is done: best-effort send; drop if the buffer is full rather than block.
				select {
				case outCh <- ev:
				default:
				}
			}
		}
	}
}

// offsetSetter is satisfied by events whose embedded *BaseEvent promotes SetOffset.
type offsetSetter interface {
	SetOffset(int64)
}

// pullOnce performs one Pull RPC against AgentEventLog. When the run has finished,
// the request uses a detached context so caller cancellation does not abort the drain.
func (h *streamHandle) pullOnce(ctx context.Context, runDone bool, offset int64) (PullResponse, error) {
	parent := ctx
	if runDone {
		parent = context.Background() // drain phase: run is done, ignore caller cancellation
	}
	pullCtx, cancel := context.WithTimeout(parent, h.rt.ingressHTTPTimeout())
	defer cancel()
	return restateingress.Object[PullRequest, PullResponse](
		h.rt.ingressClient, h.rt.eventLogServiceName, h.id, "Pull",
	).Request(pullCtx, PullRequest{Offset: &offset, Wait: false})
}

// channelReady reports whether ch is closed or has a value pending, without blocking.
func channelReady(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func terminalStreamErrorMessage(getErr error) string {
	switch {
	case errors.Is(getErr, context.DeadlineExceeded):
		return "context deadline exceeded"
	case errors.Is(getErr, context.Canceled):
		return "context canceled"
	case getErr != nil:
		return getErr.Error()
	default:
		return "run failed"
	}
}

func syntheticStreamCompleteEvent(result *types.AgentRunResult, threadID, runID, rootName string) events.AgentEvent {
	if result != nil {
		if strings.TrimSpace(result.AgentName) != "" {
			result.AgentName = strings.TrimSpace(result.AgentName)
		} else if rootName != "" {
			result.AgentName = rootName
		}
	} else if rootName != "" {
		result = &types.AgentRunResult{AgentName: rootName}
	}
	return events.NewAgentRunFinishedEvent(threadID, runID, result)
}
