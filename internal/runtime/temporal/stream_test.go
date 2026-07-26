package temporal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"go.temporal.io/sdk/contrib/workflowstreams"
	"go.temporal.io/sdk/converter"
	temporalmocks "go.temporal.io/sdk/mocks"
)

// buildRetryItem encodes a streamRetrySignal to a workflowstreams.WorkflowStreamItem on the retry topic.
func buildRetryItem(t *testing.T, messageID string, offset int64) workflowstreams.WorkflowStreamItem {
	t.Helper()
	raw, err := json.Marshal(streamRetrySignal{MessageID: messageID})
	if err != nil {
		t.Fatalf("json.Marshal retrySignal: %v", err)
	}
	payload, err := converter.GetDefaultDataConverter().ToPayload(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ToPayload: %v", err)
	}
	return workflowstreams.WorkflowStreamItem{Topic: streamTopicRetry, Data: payload, Offset: offset}
}

func TestNewStreamClientOptions(t *testing.T) {
	opts := newStreamClientOptions()
	if opts.BatchInterval != streamBatchInterval {
		t.Errorf("BatchInterval = %v, want %v", opts.BatchInterval, streamBatchInterval)
	}
	if opts.MaxBatchSize != streamMaxBatchSize {
		t.Errorf("MaxBatchSize = %v, want %v", opts.MaxBatchSize, streamMaxBatchSize)
	}
	if opts.MaxRetryDuration != streamMaxRetryDuration {
		t.Errorf("MaxRetryDuration = %v, want %v", opts.MaxRetryDuration, streamMaxRetryDuration)
	}
}

func TestNewStreamSubscribeOptions(t *testing.T) {
	opts := newStreamSubscribeOptions(42, []string{streamTopicEvents, streamTopicRetry})
	wantTopics := []string{streamTopicEvents, streamTopicRetry}
	if len(opts.Topics) != len(wantTopics) || opts.Topics[0] != wantTopics[0] || opts.Topics[1] != wantTopics[1] {
		t.Errorf("Topics = %v, want %v", opts.Topics, wantTopics)
	}
	if opts.FromOffset != 42 {
		t.Errorf("FromOffset = %d, want 42", opts.FromOffset)
	}
	if opts.PollCooldown != streamPollCooldown {
		t.Errorf("PollCooldown = %v, want %v", opts.PollCooldown, streamPollCooldown)
	}
}

// TestNewStreamClient_ClosesWithoutPublishing verifies that constructing a client and
// closing it without publishing anything issues no RPCs (the publisher buffer is empty,
// so flush is a local no-op). A bare mock with no expectations set will fail the test if
// any unexpected client method is invoked.
func TestNewStreamClient_ClosesWithoutPublishing(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	sc := newStreamClient(tc, "wf-1")
	if sc == nil {
		t.Fatal("newStreamClient returned nil")
	}
	if err := sc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tc.AssertExpectations(t)
}

// buildStreamItem encodes an AgentEvent to a workflowstreams payload the same way the real
// WorkflowStream publisher would, so decodeStreamItem can be exercised without a live server.
func buildStreamItem(t *testing.T, ev events.AgentEvent, offset int64) workflowstreams.WorkflowStreamItem {
	t.Helper()
	raw, err := ev.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	payload, err := converter.GetDefaultDataConverter().ToPayload(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ToPayload: %v", err)
	}
	return workflowstreams.WorkflowStreamItem{Topic: streamTopicEvents, Data: payload, Offset: offset}
}

func TestDecodeStreamItem_Success_SetsOffset(t *testing.T) {
	ev := events.NewAgentToolCallStartEvent("tc1", "echo")
	item := buildStreamItem(t, ev, 7)

	decoded, err := decodeStreamItem(item)
	if err != nil {
		t.Fatalf("decodeStreamItem: %v", err)
	}
	if decoded.Type() != events.AgentEventTypeToolCallStart {
		t.Fatalf("Type() = %v, want %v", decoded.Type(), events.AgentEventTypeToolCallStart)
	}
	offset, hasOffset := decoded.(*events.AgentToolCallStartEvent).Offset()
	if !hasOffset || offset != 7 {
		t.Fatalf("Offset() = (%d, %v), want (7, true)", offset, hasOffset)
	}
}

func TestDecodeStreamItem_BadPayload_ReturnsError(t *testing.T) {
	// A payload whose Data does not decode to json.RawMessage (wrong metadata/encoding).
	item := workflowstreams.WorkflowStreamItem{Topic: streamTopicEvents, Data: nil, Offset: 0}
	if _, err := decodeStreamItem(item); err == nil {
		t.Fatal("expected error for nil payload")
	}
}

func TestDecodeStreamItem_MalformedEventJSON_ReturnsError(t *testing.T) {
	payload, err := converter.GetDefaultDataConverter().ToPayload(json.RawMessage(`{"not":"an event"}`))
	if err != nil {
		t.Fatalf("ToPayload: %v", err)
	}
	item := workflowstreams.WorkflowStreamItem{Topic: streamTopicEvents, Data: payload, Offset: 1}
	if _, err := decodeStreamItem(item); err == nil {
		t.Fatal("expected error for malformed event JSON")
	}
}

// TestBuildRetryItem verifies the helper used in other retry-related tests.
func TestBuildRetryItem(t *testing.T) {
	item := buildRetryItem(t, "msg-x", 5)
	if item.Topic != streamTopicRetry {
		t.Errorf("Topic = %q, want %q", item.Topic, streamTopicRetry)
	}
	if item.Offset != 5 {
		t.Errorf("Offset = %d, want 5", item.Offset)
	}
	// Verify the payload round-trips back to a streamRetrySignal.
	var rawJSON json.RawMessage
	if err := converter.GetDefaultDataConverter().FromPayload(item.Data, &rawJSON); err != nil {
		t.Fatalf("FromPayload: %v", err)
	}
	var sig streamRetrySignal
	if err := json.Unmarshal(rawJSON, &sig); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if sig.MessageID != "msg-x" {
		t.Errorf("MessageID = %q, want msg-x", sig.MessageID)
	}
}
