package restate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	restatesdk "github.com/restatedev/sdk-go"
)

// errAgentEventLogClearedMsg is the terminal Pull error marker when the log was cleared.
// Clients treat this as end-of-stream (not a retryable failure).
const errAgentEventLogClearedMsg = "pubsub: agent event log already cleared"

// ErrAgentEventLogCleared is returned (as a Restate terminal error) when Pull targets a
// log that has been Cleared past the reader's offset.
var ErrAgentEventLogCleared = errors.New(errAgentEventLogClearedMsg)

// isAgentEventLogCleared reports whether err is (or wraps) an already-cleared Pull failure.
func isAgentEventLogCleared(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAgentEventLogCleared) {
		return true
	}
	return strings.Contains(err.Error(), errAgentEventLogClearedMsg)
}

const (
	pubsubMetaKey         = "messagesMetadata"
	pubsubSubscriptionKey = "subscription"
	pubsubPullTimeout     = 20 * time.Second // used only when PullRequest.Wait is true
	pubsubPullInterval    = time.Second      // client sleep between non-blocking peeks
)

// AgentEventLog is a Restate Virtual Object implementing a per-run durable event log
// (publish / pull / subscribe / clear). Bound beside [AgentLoop] so Stream can publish
// events and [streamHandle.Events] can pull them from any process.
//
// Protocol mirrors https://github.com/restatedev/pubsub (TypeScript @restatedev/pubsub),
// with Clear (full wipe) instead of partial Truncate — matching this product's use case.
type AgentEventLog struct {
	name string // AgentEventLog_<agentName>
}

// ServiceName returns the Restate service name for this Virtual Object.
func (s AgentEventLog) ServiceName() string {
	if s.name != "" {
		return s.name
	}
	return agentEventLogServiceName
}

type pubsubMetadata struct {
	Head int64 `json:"head"`
	Tail int64 `json:"tail"`
}

type pubsubSubscription struct {
	Offset int64  `json:"offset"`
	ID     string `json:"id"`
}

type pubsubNotification struct {
	NewOffset   int64             `json:"newOffset"`
	NewMessages []json.RawMessage `json:"newMessages"`
}

// PullRequest is the body for [AgentEventLog.Pull].
type PullRequest struct {
	// Offset is the next message index to read. Nil means wait for new messages at tail
	// (no catch-up). Zero is a valid start-of-stream offset.
	Offset *int64 `json:"offset,omitempty"`
	// Wait, when true, long-polls up to pubsubPullTimeout (Restate PubSub convention).
	// SDK stream/approval listeners use Wait=false so Pull always completes quickly and
	// is never left suspended when the process exits.
	Wait bool `json:"wait,omitempty"`
}

// PullResponse is returned by [AgentEventLog.Pull].
type PullResponse struct {
	Messages   []json.RawMessage `json:"messages"`
	NextOffset int64             `json:"nextOffset"`
}

func messageKey(i int64) string { return fmt.Sprintf("m_%d", i) }

func loadMessagesInRange(ctx restatesdk.ObjectSharedContext, fromIncluded, toExcluded int64) ([]json.RawMessage, error) {
	if fromIncluded >= toExcluded {
		return nil, nil
	}
	out := make([]json.RawMessage, 0, toExcluded-fromIncluded)
	for i := fromIncluded; i < toExcluded; i++ {
		msg, err := restatesdk.Get[json.RawMessage](ctx, messageKey(i))
		if err != nil {
			return nil, err
		}
		if len(msg) == 0 {
			return nil, restatesdk.TerminalErrorf("pubsub: missing message at offset %d", i)
		}
		out = append(out, msg)
	}
	return out, nil
}

func getMetadata(ctx restatesdk.ObjectSharedContext) (pubsubMetadata, error) {
	meta, err := restatesdk.Get[*pubsubMetadata](ctx, pubsubMetaKey)
	if err != nil {
		return pubsubMetadata{}, err
	}
	if meta == nil {
		return pubsubMetadata{}, nil
	}
	return *meta, nil
}

func getSubscriptions(ctx restatesdk.ObjectSharedContext) ([]pubsubSubscription, error) {
	subs, err := restatesdk.Get[[]pubsubSubscription](ctx, pubsubSubscriptionKey)
	if err != nil {
		return nil, err
	}
	return subs, nil
}

// Pull returns messages from offset (catch-up). When [PullRequest.Wait] is false (SDK default),
// an empty topic returns immediately with no awakeable — safe for short-lived clients.
// When Wait is true, long-polls up to pubsubPullTimeout and returns HTTP 408 on timeout.
func (s AgentEventLog) Pull(ctx restatesdk.ObjectSharedContext, req PullRequest) (PullResponse, error) {
	meta, err := getMetadata(ctx)
	if err != nil {
		return PullResponse{}, err
	}

	waitFrom := meta.Tail
	if req.Offset != nil {
		waitFrom = *req.Offset
		// After Clear, Head advances past cleared messages. Late Pulls get a terminal
		// error (not empty success) so clients stop cleanly and Restate does not retry.
		if waitFrom < meta.Head {
			return PullResponse{}, restatesdk.ToTerminalError(fmt.Errorf(
				"%w (offset %d < head %d)", ErrAgentEventLogCleared, waitFrom, meta.Head))
		}
		if waitFrom < meta.Tail {
			messages, err := loadMessagesInRange(ctx, waitFrom, meta.Tail)
			if err != nil {
				if strings.Contains(err.Error(), "missing message") {
					return PullResponse{}, restatesdk.ToTerminalError(fmt.Errorf(
						"%w: %v", ErrAgentEventLogCleared, err))
				}
				return PullResponse{}, err
			}
			return PullResponse{Messages: messages, NextOffset: meta.Tail}, nil
		}
	}

	if !req.Wait {
		return PullResponse{Messages: nil, NextOffset: waitFrom}, nil
	}

	awakeable := restatesdk.Awakeable[pubsubNotification](ctx)
	restatesdk.ObjectSend(ctx, s.ServiceName(), restatesdk.Key(ctx), "Subscribe").
		Send(pubsubSubscription{Offset: waitFrom, ID: awakeable.Id()})

	timeout := restatesdk.After(ctx, pubsubPullTimeout)
	first, waitErr := restatesdk.WaitFirst(ctx, awakeable, timeout)
	if waitErr != nil {
		return PullResponse{}, waitErr
	}
	switch first {
	case timeout:
		if err := timeout.Done(); err != nil {
			return PullResponse{}, err
		}
		return PullResponse{}, restatesdk.ToTerminalError(
			fmt.Errorf("pubsub: pull timeout"),
			restatesdk.WithErrorCode(408),
		)
	default:
		note, err := awakeable.Result()
		if err != nil {
			return PullResponse{}, err
		}
		return PullResponse{Messages: note.NewMessages, NextOffset: note.NewOffset}, nil
	}
}

// Publish appends a message and notifies waiting subscribers.
func (AgentEventLog) Publish(ctx restatesdk.ObjectContext, message json.RawMessage) error {
	meta, err := getMetadata(ctx)
	if err != nil {
		return err
	}
	restatesdk.Set(ctx, messageKey(meta.Tail), message)
	meta.Tail++
	restatesdk.Set(ctx, pubsubMetaKey, meta)

	subs, err := getSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		messages, loadErr := loadMessagesInRange(ctx, sub.Offset, meta.Tail)
		if loadErr != nil {
			return loadErr
		}
		restatesdk.ResolveAwakeable(ctx, sub.ID, pubsubNotification{
			NewOffset:   meta.Tail,
			NewMessages: messages,
		})
	}
	restatesdk.Clear(ctx, pubsubSubscriptionKey)
	return nil
}

// Subscribe registers an awakeable for future publishes, or resolves immediately when
// messages are already available at/after offset. Exclusive (mutates subscription state).
func (AgentEventLog) Subscribe(ctx restatesdk.ObjectContext, sub pubsubSubscription) error {
	meta, err := getMetadata(ctx)
	if err != nil {
		return err
	}
	if sub.Offset < meta.Head {
		restatesdk.RejectAwakeable(ctx, sub.ID, restatesdk.TerminalErrorf(
			"pubsub: offset %d is lower than the head %d", sub.Offset, meta.Head))
		return nil
	}
	if sub.Offset < meta.Tail {
		messages, loadErr := loadMessagesInRange(ctx, sub.Offset, meta.Tail)
		if loadErr != nil {
			return loadErr
		}
		restatesdk.ResolveAwakeable(ctx, sub.ID, pubsubNotification{
			NewOffset:   meta.Tail,
			NewMessages: messages,
		})
		return nil
	}
	subs, err := getSubscriptions(ctx)
	if err != nil {
		return err
	}
	subs = append(subs, sub)
	restatesdk.Set(ctx, pubsubSubscriptionKey, subs)
	return nil
}

// Clear removes all messages from this run's event log (full wipe after EventLog.TTL).
func (AgentEventLog) Clear(ctx restatesdk.ObjectContext) error {
	meta, err := getMetadata(ctx)
	if err != nil {
		return err
	}
	if meta.Tail == 0 && meta.Head == 0 {
		return nil
	}
	newHead := meta.Tail
	for i := meta.Head; i < newHead; i++ {
		restatesdk.Clear(ctx, messageKey(i))
	}
	meta.Head = newHead
	restatesdk.Set(ctx, pubsubMetaKey, meta)

	subs, err := getSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if sub.Offset < newHead {
			restatesdk.RejectAwakeable(ctx, sub.ID, restatesdk.TerminalErrorf(
				"pubsub: offset %d is lower than the head %d", sub.Offset, newHead))
		}
	}
	if len(subs) > 0 {
		restatesdk.Clear(ctx, pubsubSubscriptionKey)
	}
	return nil
}

// publishAgentEventJSON publishes one event to the run's AgentEventLog and waits
// for Publish to finish. Request (not Send) so the stream cannot complete before
// events are durable — otherwise listeners miss the last tokens.
func publishAgentEventJSON(ctx restatesdk.Context, service, runID string, eventJSON json.RawMessage) error {
	_, err := restatesdk.Object[restatesdk.Void](ctx, service, runID, "Publish").Request(eventJSON)
	return err
}
