package agent

import (
	"context"
	"testing"

	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/logger"
)

func TestAgentChannel_PublishSubscribe(t *testing.T) {
	l := logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{Level: "error"}))
	c := newAgentChannel(l)
	ctx := context.Background()

	data := []byte("hello")
	if err := c.Publish(ctx, "ch1", data); err != nil {
		t.Fatalf("Publish empty subs: %v", err)
	}

	ch, closeFn, err := c.Subscribe(ctx, "ch1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = closeFn() }()

	go func() {
		if err := c.Publish(ctx, "ch1", []byte("msg1")); err != nil {
			t.Errorf("Publish: %v", err)
		}
	}()

	got := <-ch
	if string(got) != "msg1" {
		t.Errorf("got %q, want msg1", string(got))
	}
}

func TestAgentChannel_MultipleSubscribers(t *testing.T) {
	l := logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{Level: "error"}))
	c := newAgentChannel(l)
	ctx := context.Background()

	ch1, close1, _ := c.Subscribe(ctx, "ch")
	defer func() { _ = close1() }()
	ch2, close2, _ := c.Subscribe(ctx, "ch")
	defer func() { _ = close2() }()

	if err := c.Publish(ctx, "ch", []byte("broadcast")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	g1, g2 := <-ch1, <-ch2
	if string(g1) != "broadcast" || string(g2) != "broadcast" {
		t.Errorf("got %q, %q; want broadcast for both", string(g1), string(g2))
	}
}

func TestAgentChannel_CloseUnsubscribes(t *testing.T) {
	l := logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{Level: "error"}))
	c := newAgentChannel(l)
	ctx := context.Background()

	ch, closeFn, _ := c.Subscribe(ctx, "ch")
	_ = closeFn()

	if err := c.Publish(ctx, "ch", []byte("x")); err != nil {
		t.Fatalf("Publish after close: %v", err)
	}
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed")
	}
}
