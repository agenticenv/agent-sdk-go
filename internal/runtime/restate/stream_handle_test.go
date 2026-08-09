package restate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	restateingress "github.com/restatedev/sdk-go/ingress"
)

func TestNewStreamHandle_embedsRunHandle(t *testing.T) {
	h := newStreamHandle("run-s", "inv-s", nil)
	if h.ID() != "run-s" {
		t.Fatalf("got %q", h.ID())
	}
	if h.runHandle == nil || h.invocationID != "inv-s" {
		t.Fatal("expected embedded runHandle")
	}
}

func TestStreamHandle_Approve_NilRuntime(t *testing.T) {
	h := &streamHandle{runHandle: &runHandle{id: "r"}}
	err := h.Approve(context.Background(), "tok", types.ApprovalStatusApproved)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("got %v", err)
	}
}

func TestStreamHandle_Events_NotConfigured(t *testing.T) {
	h := &streamHandle{runHandle: &runHandle{id: "r"}}
	_, err := h.Events(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("got %v", err)
	}
}

func TestStreamHandle_Events_ContextCanceled(t *testing.T) {
	rt := &RestateRuntime{ingressClient: restateingress.NewClient("http://127.0.0.1:1")}
	h := &streamHandle{runHandle: &runHandle{id: "r", rt: rt}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.Events(ctx, 0)
	if err == nil {
		t.Fatal("expected error for canceled ctx")
	}
}

func TestStreamHandle_Events_NegativeOffset(t *testing.T) {
	rt := &RestateRuntime{ingressClient: restateingress.NewClient("http://127.0.0.1:1")}
	h := &streamHandle{runHandle: &runHandle{id: "r", rt: rt}}
	_, err := h.Events(context.Background(), -1)
	if err == nil || !strings.Contains(err.Error(), "fromOffset") {
		t.Fatalf("got %v", err)
	}
}

func TestChannelReady(t *testing.T) {
	ch := make(chan struct{})
	if channelReady(ch) {
		t.Fatal("open channel should not be ready")
	}
	close(ch)
	if !channelReady(ch) {
		t.Fatal("closed channel should be ready")
	}
}

func TestTerminalStreamErrorMessage(t *testing.T) {
	if got := terminalStreamErrorMessage(context.DeadlineExceeded); got != "context deadline exceeded" {
		t.Fatalf("%q", got)
	}
	if got := terminalStreamErrorMessage(context.Canceled); got != "context canceled" {
		t.Fatalf("%q", got)
	}
	if got := terminalStreamErrorMessage(nil); got != "run failed" {
		t.Fatalf("%q", got)
	}
	if got := terminalStreamErrorMessage(errors.New("boom")); got != "boom" {
		t.Fatalf("%q", got)
	}
}

func TestSyntheticStreamCompleteEvent(t *testing.T) {
	ev := syntheticStreamCompleteEvent(&types.AgentRunResult{Content: "c"}, "", "rid", "Root")
	if ev.Type() != events.AgentEventTypeRunFinished {
		t.Fatalf("got %v", ev.Type())
	}
	ev2 := syntheticStreamCompleteEvent(nil, "", "rid", "Root")
	if ev2.Type() != events.AgentEventTypeRunFinished {
		t.Fatalf("got %v", ev2.Type())
	}
}

func TestOffsetSetter_OnDecodedEvent(t *testing.T) {
	ev := events.NewAgentTextMessageContentEvent("m1", "hi")
	os, ok := any(ev).(offsetSetter)
	if !ok {
		t.Fatal("expected TextMessageContentEvent to implement offsetSetter")
	}
	os.SetOffset(7)
	got, has := ev.Offset()
	if !has || got != 7 {
		t.Fatalf("Offset() = %d,%v want 7,true", got, has)
	}
}
