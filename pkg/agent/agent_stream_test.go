package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang/mock/gomock"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	rtmocks "github.com/agenticenv/agent-sdk-go/internal/runtime/mocks"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation"
	ifacemocks "github.com/agenticenv/agent-sdk-go/pkg/interfaces/mocks"
)

// expectRuntimeStreamHandle stubs Runtime.Stream → StreamHandle with a stable ID.
// Done is left open so awaitCompletion keeps the handle registered for the test.
func expectRuntimeStreamHandle(
	ctrl *gomock.Controller,
	mockRT *rtmocks.MockRuntime,
	runID string,
	assertReq func(*runtime.RunRequest),
) *rtmocks.MockStreamHandle {
	h := rtmocks.NewMockStreamHandle(ctrl)
	mockRT.EXPECT().Stream(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req *runtime.RunRequest) (runtime.StreamHandle, error) {
			if assertReq != nil {
				assertReq(req)
			}
			return h, nil
		},
	)
	h.EXPECT().ID().Return(runID).AnyTimes()
	done := make(chan struct{})
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()
	return h
}

func TestAgent_Stream_SetsEnableLLMStream(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	const runID = "stream-1"
	var streamReq *runtime.RunRequest
	h := expectRuntimeStreamHandle(ctrl, mockRT, runID, func(req *runtime.RunRequest) {
		streamReq = req
	})
	finCh := make(chan events.AgentEvent, 2)
	finCh <- events.NewAgentRunFinishedEvent("", "", &types.AgentRunResult{AgentName: "TestAgent", Content: "done"})
	close(finCh)
	var finRecv <-chan events.AgentEvent = finCh
	h.EXPECT().Events(gomock.Any(), int64(0)).Return(finRecv, nil)

	a := testAgentWithRuntime(mockRT)
	agentStream, err := a.Stream(context.Background(), "prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if agentStream.ID() != runID {
		t.Fatalf("Stream ID = %q, want %q", agentStream.ID(), runID)
	}
	ch, err := agentStream.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for range ch {
		}
	}()
	if streamReq == nil || !streamReq.EnableLLMStream {
		t.Fatalf("Stream request = %+v", streamReq)
	}
	if streamReq.UserPrompt != "prompt" {
		t.Errorf("UserPrompt = %q", streamReq.UserPrompt)
	}
	if ch == nil {
		t.Fatal("Stream returned nil channel")
	}
	ev := <-ch
	if ev == nil {
		t.Fatal("nil event")
	}
	if ev.Type() != events.AgentEventTypeRunFinished {
		t.Fatalf("want RunFinished, got type %v", ev.Type())
	}
	fin, ok := ev.(*events.AgentRunFinishedEvent)
	if !ok || fin == nil {
		t.Fatalf("event not *AgentRunFinishedEvent: %+v", ev)
	}
	result := fin.Result
	if result == nil {
		t.Fatalf("Result is nil")
	}
	if result.Content != "done" {
		t.Fatalf("result.Content = %q", result.Content)
	}
}

func TestAgent_Stream_DeliversTextEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	h := expectRuntimeStreamHandle(ctrl, mockRT, "stream-text", nil)
	textCh := make(chan events.AgentEvent, 1)
	textCh <- events.NewAgentTextMessageContentEvent("", "partial")
	close(textCh)
	var textRecv <-chan events.AgentEvent = textCh
	h.EXPECT().Events(gomock.Any(), int64(0)).Return(textRecv, nil)

	a := testAgentWithRuntime(mockRT)
	agentStream, err := a.Stream(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := agentStream.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ev := <-ch
	if ev == nil {
		t.Fatal("expected event, got nil")
	}
	if ev.Type() != events.AgentEventTypeTextMessageContent {
		t.Fatalf("event type = %v, want TextMessageContent", ev.Type())
	}
	textEv, ok := ev.(*events.AgentTextMessageContentEvent)
	if !ok {
		t.Fatalf("event not *AgentTextMessageContentEvent: %T", ev)
	}
	if textEv.Delta != "partial" {
		t.Fatalf("Delta = %q, want partial", textEv.Delta)
	}
}

func TestAgent_Stream_RejectsMissingConversationID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	a := testAgentWithRuntime(rtmocks.NewMockRuntime(ctrl))
	a.conversationConfig = &conversation.Config{Conversation: ifacemocks.NewMockConversation(ctrl)}
	_, err := a.Stream(context.Background(), "prompt", nil)
	if err == nil {
		t.Fatal("expected error when conversation configured but opts nil")
	}
}

func TestAgent_GetAgentStream_ReturnsSameHandleInProcess(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	_ = expectRuntimeStreamHandle(ctrl, mockRT, "stream-shared", nil)

	a := testAgentWithRuntime(mockRT)
	h1, err := a.Stream(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := a.GetAgentStream(context.Background(), h1.ID())
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("GetAgentStream must return the same in-process handle as Stream")
	}
	h3, err := a.GetAgentStream(context.Background(), h1.ID())
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h3 {
		t.Fatal("repeated GetAgentStream must return the same handle")
	}
}

// TestAgent_GetAgentStream_LiveRegistryNoRuntimeRPC asserts same-process reuse of a live
// (non-Done) registry entry does not call Runtime.GetStreamHandle or StreamHandle.Status.
func TestAgent_GetAgentStream_LiveRegistryNoRuntimeRPC(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	_ = expectRuntimeStreamHandle(ctrl, mockRT, "stream-live-no-rpc", nil)
	// No GetStreamHandle / Status expectations: unexpected calls fail the test via gomock.

	a := testAgentWithRuntime(mockRT)
	h1, err := a.Stream(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := a.GetAgentStream(context.Background(), h1.ID())
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("expected in-process registry reuse")
	}
}

func TestAgent_GetAgentStream_EventsPassesOffset(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	h := rtmocks.NewMockStreamHandle(ctrl)
	mockRT.EXPECT().GetStreamHandle(gomock.Any(), "saved-run").Return(h, nil)
	h.EXPECT().ID().Return("saved-run").AnyTimes()
	done := make(chan struct{})
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()
	ch := make(chan events.AgentEvent)
	close(ch)
	var recv <-chan events.AgentEvent = ch
	h.EXPECT().Events(gomock.Any(), int64(42)).Return(recv, nil)

	a := testAgentWithRuntime(mockRT)
	agentStream, err := a.GetAgentStream(context.Background(), "saved-run")
	if err != nil {
		t.Fatal(err)
	}
	if agentStream.ID() != "saved-run" {
		t.Fatalf("ID = %q", agentStream.ID())
	}
	got, err := agentStream.Events(context.Background(), WithOffset(42))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil channel")
	}
}

func TestAgent_GetAgentStream_TerminalReturnsAlreadyCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().GetStreamHandle(gomock.Any(), "done-stream").Return(nil, types.ErrRunAlreadyCompleted)

	a := testAgentWithRuntime(mockRT)
	agentStream, err := a.GetAgentStream(context.Background(), "done-stream")
	if !errors.Is(err, ErrRunAlreadyCompleted) {
		t.Fatalf("err = %v, want ErrRunAlreadyCompleted", err)
	}
	if agentStream != nil {
		t.Fatal("expected nil handle when run already completed")
	}
}

func TestAgent_GetAgentStream_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().GetStreamHandle(gomock.Any(), "gone").Return(nil, types.ErrStreamNotFound)

	a := testAgentWithRuntime(mockRT)
	agentStream, err := a.GetAgentStream(context.Background(), "gone")
	if !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("err = %v, want ErrStreamNotFound", err)
	}
	if agentStream != nil {
		t.Fatal("expected nil handle when stream reconnect is unsupported")
	}
}

func TestAgent_GetAgentStream_StatusAndCancelDelegate(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	h := rtmocks.NewMockStreamHandle(ctrl)
	mockRT.EXPECT().GetStreamHandle(gomock.Any(), "s1").Return(h, nil)
	h.EXPECT().ID().Return("s1").AnyTimes()
	done := make(chan struct{})
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()
	h.EXPECT().Status(gomock.Any()).Return(types.StatusRunning, nil)
	h.EXPECT().Cancel(gomock.Any()).Return(nil)

	a := testAgentWithRuntime(mockRT)
	agentStream, err := a.GetAgentStream(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	status, err := agentStream.Status(context.Background())
	if err != nil || status != StatusRunning {
		t.Fatalf("Status = %v, %v", status, err)
	}
	if err := agentStream.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAgent_Stream_GetAndDone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	const runID = "stream-get"
	h := expectRuntimeStreamHandle(ctrl, mockRT, runID, nil)
	h.EXPECT().Get(gomock.Any()).Return(&types.AgentRunResult{Content: "final", RunID: runID}, nil)

	a := testAgentWithRuntime(mockRT)
	agentStream, err := a.Stream(context.Background(), "prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-agentStream.Done():
		t.Fatal("Done should still be open while stream is live")
	default:
	}
	result, err := agentStream.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Content != "final" || result.RunID != runID {
		t.Fatalf("Get = %+v", result)
	}
}

func TestAgent_GetAgentStream_UnregistersWhenDone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	const runID = "stream-done-unreg"
	h := rtmocks.NewMockStreamHandle(ctrl)
	done := make(chan struct{})
	close(done)
	mockRT.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(h, nil)
	h.EXPECT().ID().Return(runID).AnyTimes()
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()
	mockRT.EXPECT().GetStreamHandle(gomock.Any(), runID).Return(nil, types.ErrRunAlreadyCompleted)

	a := testAgentWithRuntime(mockRT)
	h1, err := a.Stream(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-h1.Done()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := a.streams.Get(h1.ID()); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for stream registry unregister after Done")
		}
		time.Sleep(5 * time.Millisecond)
	}

	h2, err := a.GetAgentStream(context.Background(), h1.ID())
	if !errors.Is(err, ErrRunAlreadyCompleted) {
		t.Fatalf("err = %v, want ErrRunAlreadyCompleted", err)
	}
	if h2 != nil {
		t.Fatal("expected nil handle after stream completed")
	}
}

func TestNewAgentStream_NilRegistry_NoTracking(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	h := rtmocks.NewMockStreamHandle(ctrl)
	h.EXPECT().ID().Return("stream-nil-reg").AnyTimes()

	s := newAgentStream(h, nil)
	if s == nil || s.ID() != "stream-nil-reg" {
		t.Fatalf("newAgentStream(nil registry) = %+v", s)
	}
}

func TestNewAgentStream_RegistersAndUnregistersOnDone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const runID = "stream-reg"
	h := rtmocks.NewMockStreamHandle(ctrl)
	done := make(chan struct{})
	h.EXPECT().ID().Return(runID).AnyTimes()
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()

	streams := store.NewKV[string, *agentStream]()
	s := newAgentStream(h, streams)
	got, ok := streams.Get(runID)
	if !ok || got != s {
		t.Fatal("newAgentStream must register handle in streams map")
	}

	close(done)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := streams.Get(runID); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for DeleteIf after Done")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewAgentStream_DeleteIfKeepsReplacement(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const runID = "stream-replace"

	h1 := rtmocks.NewMockStreamHandle(ctrl)
	done1 := make(chan struct{})
	h1.EXPECT().ID().Return(runID).AnyTimes()
	h1.EXPECT().Done().Return((<-chan struct{})(done1)).AnyTimes()

	h2 := rtmocks.NewMockStreamHandle(ctrl)
	done2 := make(chan struct{})
	h2.EXPECT().ID().Return(runID).AnyTimes()
	h2.EXPECT().Done().Return((<-chan struct{})(done2)).AnyTimes()

	streams := store.NewKV[string, *agentStream]()
	_ = newAgentStream(h1, streams)
	s2 := newAgentStream(h2, streams)
	if got, _ := streams.Get(runID); got != s2 {
		t.Fatal("map should hold the replacement handle")
	}

	close(done1)
	time.Sleep(50 * time.Millisecond)
	if got, ok := streams.Get(runID); !ok || got != s2 {
		t.Fatal("finished predecessor must not remove replacement via DeleteIf")
	}

	close(done2)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := streams.Get(runID); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for replacement unregister")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAgent_Stream_DisableTokenStreaming(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	var streamReq *runtime.RunRequest
	_ = expectRuntimeStreamHandle(ctrl, mockRT, "stream-no-tokens", func(req *runtime.RunRequest) {
		streamReq = req
	})

	a := testAgentWithRuntime(mockRT)
	_, err := a.Stream(context.Background(), "prompt", &AgentStreamOptions{DisableTokenStreaming: true})
	if err != nil {
		t.Fatal(err)
	}
	if streamReq == nil || streamReq.EnableLLMStream {
		t.Fatalf("expected EnableLLMStream=false, got %+v", streamReq)
	}
}

func TestWithOffset_SetsFromOffset(t *testing.T) {
	cfg := &agentStreamConfig{}
	WithOffset(99)(cfg)
	if cfg.fromOffset != 99 {
		t.Fatalf("fromOffset = %d, want 99", cfg.fromOffset)
	}
}
