package eventbus

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func TestInmem_PublishSubscribe(t *testing.T) {
	c := NewInmem(logger.NoopLogger())
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

func TestInmem_MultipleSubscribers(t *testing.T) {
	c := NewInmem(logger.NoopLogger())
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

func TestInmem_CloseUnsubscribes(t *testing.T) {
	c := NewInmem(logger.NoopLogger())
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

func TestInmem_Close_ClosesSubscribers(t *testing.T) {
	c := NewInmem(logger.NoopLogger())
	ctx := context.Background()

	ch, closeFn, err := c.Subscribe(ctx, "ch")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	c.Close()
	c.Close() // idempotent

	_, ok := <-ch
	if ok {
		t.Error("subscriber channel should be closed after Close")
	}
	if err := closeFn(); err != nil {
		t.Fatalf("closeFn after Close: %v", err)
	}
	if err := c.Publish(ctx, "ch", []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Publish after Close: got %v, want ErrClosed", err)
	}
	if _, _, err := c.Subscribe(ctx, "ch"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe after Close: got %v, want ErrClosed", err)
	}
}

// TestInmem_PublishRacesUnsubscribe reproduces the CI panic: Publish snapshots a
// channel's subscriber list, then sends to each one without the lock held. If a
// subscriber's closeFn (Unsubscribe) removes+closes its channel in that window, Publish
// must not panic with "send on closed channel" — it should just drop that delivery.
// Run with -race; a single iteration is not guaranteed to hit the window, so this fans
// out many concurrent publish/unsubscribe pairs on fresh subscriptions each round.
func TestInmem_PublishRacesUnsubscribe(t *testing.T) {
	c := NewInmem(logger.NoopLogger())
	ctx := context.Background()

	const rounds = 200
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		ch, closeFn, err := c.Subscribe(ctx, "ch")
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := c.Publish(ctx, "ch", []byte("x")); err != nil {
				t.Errorf("Publish: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			_ = closeFn()
		}()
		// Drain to avoid blocking Publish on a full buffer, ignoring close.
		go func() { //nolint:errcheck
			for range ch {
			}
		}()
	}
	wg.Wait()
}
