package restate

import (
	"encoding/json"
	"fmt"
	"testing"

	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/x/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAgentEventLog_ServiceName(t *testing.T) {
	if got := (AgentEventLog{}).ServiceName(); got != agentEventLogServiceName {
		t.Fatalf("got %q want %q", got, agentEventLogServiceName)
	}
}

func TestMessageKey(t *testing.T) {
	if got := messageKey(0); got != "m_0" {
		t.Fatalf("got %q", got)
	}
	if got := messageKey(42); got != "m_42" {
		t.Fatalf("got %q", got)
	}
}

func TestPubsubTypes_JSONRoundTrip(t *testing.T) {
	meta := pubsubMetadata{Head: 1, Tail: 5}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var got pubsubMetadata
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != meta {
		t.Fatalf("got %#v want %#v", got, meta)
	}

	off := int64(3)
	req := PullRequest{Offset: &off, Wait: false}
	b, err = json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var gotReq PullRequest
	if err := json.Unmarshal(b, &gotReq); err != nil {
		t.Fatal(err)
	}
	if gotReq.Offset == nil || *gotReq.Offset != 3 || gotReq.Wait {
		t.Fatalf("got %#v", gotReq)
	}

	resp := PullResponse{
		Messages:   []json.RawMessage{json.RawMessage(`{"a":1}`)},
		NextOffset: 4,
	}
	b, err = json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var gotResp PullResponse
	if err := json.Unmarshal(b, &gotResp); err != nil {
		t.Fatal(err)
	}
	if gotResp.NextOffset != 4 || len(gotResp.Messages) != 1 {
		t.Fatalf("got %#v", gotResp)
	}
}

func TestAgentEventLog_Pull_EmptyNoWait(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Get(pubsubMetaKey, mock.Anything).Return(false, nil)
	resp, err := (AgentEventLog{}).Pull(restatesdk.WithMockContext(ctx), PullRequest{Wait: false})
	require.NoError(t, err)
	require.Empty(t, resp.Messages)
}

func TestAgentEventLog_Pull_CatchUp(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().GetAndReturn(pubsubMetaKey, &pubsubMetadata{Head: 0, Tail: 2})
	ctx.EXPECT().GetAndReturn(messageKey(0), json.RawMessage(`{"a":1}`))
	ctx.EXPECT().GetAndReturn(messageKey(1), json.RawMessage(`{"b":2}`))
	off := int64(0)
	resp, err := (AgentEventLog{}).Pull(restatesdk.WithMockContext(ctx), PullRequest{Offset: &off})
	require.NoError(t, err)
	require.Len(t, resp.Messages, 2)
	require.Equal(t, int64(2), resp.NextOffset)
}

func TestAgentEventLog_Pull_OffsetBelowHead(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().GetAndReturn(pubsubMetaKey, &pubsubMetadata{Head: 5, Tail: 5})
	off := int64(2)
	_, err := (AgentEventLog{}).Pull(restatesdk.WithMockContext(ctx), PullRequest{Offset: &off})
	require.Error(t, err)
	require.True(t, restatesdk.IsTerminalError(err))
	require.True(t, isAgentEventLogCleared(err))
}

func TestAgentEventLog_Pull_MissingMessageAlreadyCleared(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().GetAndReturn(pubsubMetaKey, &pubsubMetadata{Head: 0, Tail: 3})
	ctx.EXPECT().GetAndReturn(messageKey(0), json.RawMessage(`{"a":1}`))
	ctx.EXPECT().Get(messageKey(1), mock.Anything).Return(false, nil) // missing / cleared
	off := int64(0)
	_, err := (AgentEventLog{}).Pull(restatesdk.WithMockContext(ctx), PullRequest{Offset: &off})
	require.Error(t, err)
	require.True(t, restatesdk.IsTerminalError(err))
	require.True(t, isAgentEventLogCleared(err))
}

func TestIsAgentEventLogCleared(t *testing.T) {
	require.False(t, isAgentEventLogCleared(nil))
	require.True(t, isAgentEventLogCleared(ErrAgentEventLogCleared))
	require.True(t, isAgentEventLogCleared(fmt.Errorf("%w (offset 1 < head 2)", ErrAgentEventLogCleared)))
	require.False(t, isAgentEventLogCleared(fmt.Errorf("network down")))
}

func TestAgentEventLog_Publish_NoSubs(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Get(pubsubMetaKey, mock.Anything).Return(false, nil)
	ctx.EXPECT().Set(messageKey(0), mock.Anything).Once()
	ctx.EXPECT().Set(pubsubMetaKey, pubsubMetadata{Head: 0, Tail: 1}).Once()
	ctx.EXPECT().Get(pubsubSubscriptionKey, mock.Anything).Return(false, nil)
	ctx.EXPECT().Clear(pubsubSubscriptionKey).Once()
	require.NoError(t, (AgentEventLog{}).Publish(restatesdk.WithMockContext(ctx), json.RawMessage(`{"x":1}`)))
}

func TestAgentEventLog_Subscribe_ImmediateAndReject(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().GetAndReturn(pubsubMetaKey, &pubsubMetadata{Head: 0, Tail: 1})
	ctx.EXPECT().GetAndReturn(messageKey(0), json.RawMessage(`"m"`))
	ctx.EXPECT().ResolveAwakeable("sid", mock.Anything).Once()
	require.NoError(t, (AgentEventLog{}).Subscribe(restatesdk.WithMockContext(ctx), pubsubSubscription{Offset: 0, ID: "sid"}))

	ctx2 := mocks.NewMockContext(t)
	ctx2.EXPECT().GetAndReturn(pubsubMetaKey, &pubsubMetadata{Head: 4, Tail: 4})
	ctx2.EXPECT().RejectAwakeable("sid", mock.Anything).Once()
	require.NoError(t, (AgentEventLog{}).Subscribe(restatesdk.WithMockContext(ctx2), pubsubSubscription{Offset: 1, ID: "sid"}))
}

func TestAgentEventLog_Subscribe_Append(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().GetAndReturn(pubsubMetaKey, &pubsubMetadata{Head: 0, Tail: 0})
	ctx.EXPECT().Get(pubsubSubscriptionKey, mock.Anything).Return(false, nil)
	ctx.EXPECT().Set(pubsubSubscriptionKey, []pubsubSubscription{{Offset: 0, ID: "sid"}}).Once()
	require.NoError(t, (AgentEventLog{}).Subscribe(restatesdk.WithMockContext(ctx), pubsubSubscription{Offset: 0, ID: "sid"}))
}

func TestAgentEventLog_Clear(t *testing.T) {
	ctxEmpty := mocks.NewMockContext(t)
	ctxEmpty.EXPECT().Get(pubsubMetaKey, mock.Anything).Return(false, nil)
	require.NoError(t, (AgentEventLog{}).Clear(restatesdk.WithMockContext(ctxEmpty)))

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().GetAndReturn(pubsubMetaKey, &pubsubMetadata{Head: 0, Tail: 2})
	ctx.EXPECT().Clear(messageKey(0)).Once()
	ctx.EXPECT().Clear(messageKey(1)).Once()
	ctx.EXPECT().Set(pubsubMetaKey, pubsubMetadata{Head: 2, Tail: 2}).Once()
	ctx.EXPECT().Get(pubsubSubscriptionKey, mock.Anything).Return(false, nil)
	require.NoError(t, (AgentEventLog{}).Clear(restatesdk.WithMockContext(ctx)))
}

func TestLoadMessagesInRange_Empty(t *testing.T) {
	msgs, err := loadMessagesInRange(restatesdk.WithMockContext(mocks.NewMockContext(t)), 5, 5)
	require.NoError(t, err)
	require.Nil(t, msgs)
}

func TestPublishAgentEventJSON(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	client := ctx.EXPECT().MockObjectClient(agentEventLogServiceName, "run-x", "Publish")
	client.RequestAndReturn(mock.Anything, restatesdk.Void{}, nil)
	require.NoError(t, publishAgentEventJSON(restatesdk.WithMockContext(ctx), agentEventLogServiceName, "run-x", json.RawMessage(`{}`)))
}
