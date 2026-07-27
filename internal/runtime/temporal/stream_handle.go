package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"go.temporal.io/sdk/converter"
)

var _ sdkruntime.StreamHandle = (*streamHandle)(nil)

// streamHandle is the TemporalRuntime implementation of [sdkruntime.StreamHandle].
//
// It embeds [*runHandle] for ID/Status/Cancel/Get/Done (await via getRunResult).
// Events opens a WorkflowStream subscription (supports fromOffset for reconnect).
//
// Lifecycle: TemporalRuntime.Stream and TemporalRuntime.GetStreamHandle both create
// the handle with a real run-context cancel and activeStreams cleanup. Same-process
// reconnect reuses the existing activeStreams entry; crash reconnect that builds a
// new handle also gets a fresh cancel, so Cancel works after recovery. Cleanup runs
// once from awaitCompletion when the workflow finishes (not from Events). Unlike
// LocalRuntime, Events may be called more than once with a non-zero fromOffset; each
// call opens a new subscription. Known gap (see TODOs on GetStreamHandle): reconnect
// does not yet reject a fingerprint/config mismatch after redeploy.
type streamHandle struct {
	*runHandle

	threadID string
}

// newStreamHandle creates a stream handle for runID / workflowID.
// threadID is used for synthetic RUN_STARTED / RUN_FINISHED events.
// cancel is the run context cancel from Stream/GetStreamHandle (nil only in tests).
// cleanup is passed to newRunHandle and runs once from awaitCompletion.
func newStreamHandle(
	id, workflowID, threadID string,
	rt *TemporalRuntime,
	cancel context.CancelFunc,
	cleanup func(),
) *streamHandle {
	return &streamHandle{
		runHandle: newRunHandle(id, workflowID, rt, cancel, cleanup),
		threadID:  threadID,
	}
}

// Approve completes a pending tool or delegation approval for this stream run.
// Pass the approval token from the CUSTOM event Value and the chosen status.
//
// Returns [types.ErrApprovalAlreadyResolved] when the approval token refers to an activity that
// has already been completed. This can happen after Events (reconnect) replays a CUSTOM event for an
// approval that was resolved while the subscriber was disconnected. Treat it as informational.
func (h *streamHandle) Approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	if h.rt == nil {
		return fmt.Errorf("temporal: stream handle %q is not configured", h.id)
	}
	return h.rt.approve(ctx, approvalToken, status)
}

// Events subscribes to this run's event stream starting at fromOffset.
// fromOffset == 0 emits synthetic RUN_STARTED; fromOffset > 0 resumes after reconnect.
// Returns [types.ErrRunNotFound] / [types.ErrRunAlreadyCompleted] from the
// pre-subscribe workflow describe check.
func (h *streamHandle) Events(ctx context.Context, fromOffset int64) (<-chan events.AgentEvent, error) {
	if h.rt == nil || h.rt.temporalClient == nil {
		return nil, fmt.Errorf("temporal: stream handle %q is not configured for Events", h.id)
	}

	status, err := h.rt.getRunStatus(ctx, h.workflowID)
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

// deliverEvents subscribes to the WorkflowStream at fromOffset, forwards decoded AgentEvents
// to outCh until the subscription ends (workflow terminal, ctx done, or error), then emits a
// terminal synthetic lifecycle event via Get.
//
// Retry signals (streamTopicRetry) are consumed internally: they mark the associated messageID as
// stale so content events from a failed LLM activity attempt are discarded; the new attempt's
// TEXT_MESSAGE_START clears the stale flag and resumes normal forwarding.
//
// Approval dedup (fromOffset > 0): CUSTOM approval events for already-resolved approvals are
// silently dropped (via isPendingApproval) so a reconnecting subscriber does not re-prompt.
// Empty workflowID disables the query and forwards all CUSTOM events unconditionally.
func (h *streamHandle) deliverEvents(ctx context.Context, fromOffset int64, outCh chan<- events.AgentEvent) {
	defer close(outCh)

	rootName := agentNameFromRuntime(h.rt)

	if fromOffset == 0 {
		outCh <- events.NewAgentRunStartedEvent(h.threadID, h.id)
	}

	sc := newStreamClient(h.rt.temporalClient, h.workflowID)
	defer func() { _ = sc.Close(context.Background()) }()

	// staleMessageIDs tracks messageIDs whose prior attempt tokens should be discarded.
	// Cleared per-messageID when the new attempt's TEXT/REASONING _START event arrives.
	staleMessageIDs := make(map[string]struct{})

	for item, err := range sc.Subscribe(ctx, newStreamSubscribeOptions(fromOffset, []string{streamTopicEvents, streamTopicRetry})) {
		if err != nil {
			if ctx.Err() != nil {
				// Caller disconnected (ctx cancelled) — exit cleanly without touching the run.
				return
			}
			// Genuine subscribe failure (not ctx cancellation, and not a clean terminal end,
			// which the iterator reports by ending the loop without an error). Break out and
			// still attempt to deliver a terminal event below, so the channel-close contract
			// (RUN_FINISHED/RUN_ERROR before close) holds even when the stream connection itself failed.
			h.rt.logger.Warn(ctx, "stream: subscribe error, falling back to terminal result fetch",
				slog.String("scope", "runtime"),
				slog.Any("error", err))
			break
		}

		// Handle retry control signals: mark the messageID as stale, do not forward.
		if item.Topic == streamTopicRetry {
			var sig streamRetrySignal
			var rawJSON json.RawMessage
			if convErr := converter.GetDefaultDataConverter().FromPayload(item.Data, &rawJSON); convErr == nil {
				if jsonErr := json.Unmarshal(rawJSON, &sig); jsonErr == nil && sig.MessageID != "" {
					staleMessageIDs[sig.MessageID] = struct{}{}
					h.rt.logger.Debug(ctx, "stream: retry signal received, discarding prior attempt tokens",
						slog.String("scope", "runtime"),
						slog.String("messageID", sig.MessageID))
				}
			}
			continue
		}

		ev, decErr := decodeStreamItem(item)
		if decErr != nil {
			h.rt.logger.Warn(ctx, "stream: decode item skipped",
				slog.String("scope", "runtime"),
				slog.Any("error", decErr))
			continue
		}

		// Discard events for stale messageIDs (partial tokens from a failed LLM activity attempt).
		// The stale flag is cleared when the new attempt's start event arrives for that messageID,
		// so delivery resumes normally from TEXT_MESSAGE_START / REASONING_MESSAGE_START onward.
		if msgID := messageIDOf(ev); msgID != "" {
			if _, stale := staleMessageIDs[msgID]; stale {
				switch ev.Type() {
				case events.AgentEventTypeTextMessageStart, events.AgentEventTypeReasoningMessageStart:
					// New attempt started — clear stale flag and fall through to forward this event.
					delete(staleMessageIDs, msgID)
				default:
					continue // discard tokens from the failed attempt
				}
			}
		}

		// On reconnect (fromOffset > 0), skip CUSTOM approval events whose approval has already
		// been resolved. This prevents re-prompting the user for approvals that completed while
		// the subscriber was disconnected. If the query fails we err on the side of forwarding.
		if fromOffset > 0 && h.workflowID != "" && ev.Type() == events.AgentEventTypeCustom {
			if toolCallID, ok := approvalToolCallIDOf(ev); ok {
				if !h.rt.isPendingApproval(ctx, h.workflowID, toolCallID) {
					h.rt.logger.Debug(ctx, "stream: skipping already-resolved approval on reconnect",
						slog.String("scope", "runtime"),
						slog.String("toolCallID", toolCallID))
					continue
				}
			}
		}

		select {
		case outCh <- ev:
		case <-ctx.Done():
			return
		}
	}

	result, err := h.Get(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			termCtx, termCancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = h.rt.temporalClient.TerminateWorkflow(termCtx, h.workflowID, "", "run timeout")
			termCancel()
			outCh <- events.NewAgentRunErrorEvent("request timed out (approval expired or deadline exceeded)")
			return
		}
		outCh <- events.NewAgentRunErrorEvent(err.Error())
		return
	}
	outCh <- syntheticStreamCompleteEvent(result, h.threadID, h.id, rootName)
}

// syntheticStreamCompleteEvent builds a root [RUN_FINISHED] event from the workflow result.
// [streamHandle.Events] emits it after all stream events have been delivered.
func syntheticStreamCompleteEvent(result *types.AgentRunResult, threadID, runID, rootName string) events.AgentEvent {
	if result != nil {
		if strings.TrimSpace(result.AgentName) != "" {
			result.AgentName = strings.TrimSpace(result.AgentName)
		} else if strings.TrimSpace(rootName) != "" {
			result.AgentName = strings.TrimSpace(rootName)
		}
	} else if strings.TrimSpace(rootName) != "" {
		result = &types.AgentRunResult{
			AgentName: strings.TrimSpace(rootName),
		}
	}
	return events.NewAgentRunFinishedEvent(threadID, runID, result)
}

// approvalToolCallIDOf extracts the workflow-side ToolCallID from a CUSTOM approval or delegation
// event. Returns ("", false) for non-approval events or parse errors.
func approvalToolCallIDOf(ev events.AgentEvent) (string, bool) {
	ce, ok := ev.(*events.AgentCustomEvent)
	if !ok || ce == nil {
		return "", false
	}
	switch events.AgentCustomEventName(ce.Name) {
	case events.AgentCustomEventNameToolApproval:
		val, err := events.ParseCustomEventApproval(ce)
		if err != nil || val.ToolCallID == "" {
			return "", false
		}
		return val.ToolCallID, true
	case events.AgentCustomEventNameSubAgentDelegation:
		val, err := events.ParseCustomEventDelegation(ce)
		if err != nil || val.ToolCallID == "" {
			return "", false
		}
		return val.ToolCallID, true
	}
	return "", false
}

// messageIDOf returns the messageID embedded in text/reasoning message events, or "" for other types.
// Used by the forwarder to correlate retry signals with the events they invalidate.
func messageIDOf(ev events.AgentEvent) string {
	switch e := ev.(type) {
	case *events.AgentTextMessageStartEvent:
		return e.MessageID
	case *events.AgentTextMessageContentEvent:
		return e.MessageID
	case *events.AgentTextMessageEndEvent:
		return e.MessageID
	case *events.AgentReasoningMessageStartEvent:
		return e.MessageID
	case *events.AgentReasoningMessageContentEvent:
		return e.MessageID
	case *events.AgentReasoningMessageEndEvent:
		return e.MessageID
	}
	return ""
}
