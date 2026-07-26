package temporal

import (
	"encoding/json"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/workflowstreams"
	"go.temporal.io/sdk/converter"
)

// streamTopicEvents carries all AG-UI agent events (tool calls, text, reasoning, custom/approval).
const streamTopicEvents = "events"

// streamTopicRetry is a control topic published by LLM activities on Attempt > 1.
// Each item signals that the subscriber should discard any already-forwarded content events
// for the given messageID, because a prior failed attempt may have emitted partial tokens.
// Items on this topic are never forwarded to the caller; they are consumed internally by the forwarder.
const streamTopicRetry = "retry"

// streamRetrySignal is the payload published on streamTopicRetry.
type streamRetrySignal struct {
	MessageID string `json:"message_id"`
}

// Stream configuration constants. Adjust these to tune throughput vs. latency trade-offs.
// All values are intentionally in a single block so operators can find and change them easily.
const (
	// streamBatchInterval is the maximum time the client-side publisher waits before flushing
	// a batch of events to the workflow stream signal. Lower values reduce event latency;
	// higher values reduce signal traffic. Default matches the workflowstreams package default.
	streamBatchInterval = 2 * time.Second

	// streamPollCooldown is the minimum pause between subscriber polls when no new items are
	// immediately available. Shorter values reduce consumer latency at the cost of more
	// UpdateWorkflow RPCs. Default matches the workflowstreams package default.
	streamPollCooldown = 100 * time.Millisecond

	// streamMaxRetryDuration is how long a publisher retries a failed flush before giving up.
	// Must be less than streamPublisherTTL so the workflow can still deduplicate replayed batches.
	// Default matches the workflowstreams package default.
	streamMaxRetryDuration = 10 * time.Minute

	// streamPublisherTTL is used by WorkflowStream.GetState to drop stale publisher dedup entries
	// during a ContinueAsNew. Any publisher that has not sent a batch within this window is
	// pruned from the carry-forward state. Default matches the workflowstreams package default.
	streamPublisherTTL = 15 * time.Minute

	// streamMaxBatchSize triggers an immediate flush once the publisher buffer reaches this many
	// items. Zero disables size-triggered flushing (time-based only). Tune down for lower latency
	// on high-throughput runs; tune up (or zero) to reduce signal traffic on low-throughput runs.
	streamMaxBatchSize = 100
)

// newStreamClientOptions returns Options for workflowstreams.NewClient with project-wide defaults.
func newStreamClientOptions() workflowstreams.Options {
	return workflowstreams.Options{
		BatchInterval:    streamBatchInterval,
		MaxBatchSize:     streamMaxBatchSize,
		MaxRetryDuration: streamMaxRetryDuration,
	}
}

// newStreamSubscribeOptions returns SubscribeOptions that poll both agent event and retry-signal
// topics from fromOffset. The retry topic delivers internal control signals (never forwarded to callers).
func newStreamSubscribeOptions(fromOffset int64, topics []string) workflowstreams.SubscribeOptions {
	return workflowstreams.SubscribeOptions{
		Topics:       topics,
		FromOffset:   fromOffset,
		PollCooldown: streamPollCooldown,
	}
}

// newStreamClient creates a workflowstreams.Client targeting workflowID using project-wide defaults.
func newStreamClient(c client.Client, workflowID string) *workflowstreams.Client {
	return workflowstreams.NewClient(c, workflowID, newStreamClientOptions())
}

// offsetSetter is satisfied by any event whose embedded *BaseEvent has SetOffset promoted.
// All concrete AgentEvent types in the events package embed *BaseEvent by pointer, so they
// all implement this interface through method promotion.
type offsetSetter interface {
	SetOffset(int64)
}

// decodeStreamItem decodes a WorkflowStreamItem into an AgentEvent and attaches the stream offset.
// The item's Data must be a Temporal JSON payload whose Data field is the raw AG-UI event JSON.
func decodeStreamItem(item workflowstreams.WorkflowStreamItem) (events.AgentEvent, error) {
	var rawJSON json.RawMessage
	if err := converter.GetDefaultDataConverter().FromPayload(item.Data, &rawJSON); err != nil {
		return nil, err
	}
	ev, err := events.EventFromJSON(rawJSON)
	if err != nil {
		return nil, err
	}
	// Attach the stream offset so callers can pass it to Events(WithOffset).
	if os, ok := ev.(offsetSetter); ok {
		os.SetOffset(item.Offset)
	}
	return ev, nil
}
