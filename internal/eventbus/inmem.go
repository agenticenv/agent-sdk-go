package eventbus

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

// ErrClosed is returned by Publish/Subscribe after [Inmem.Close].
var ErrClosed = errors.New("eventbus: closed")

// Inmem is a process-local pub/sub suitable for streaming and approval fan-in on one host.
type Inmem struct {
	mu     sync.Mutex
	subs   map[string][]chan []byte
	logger logger.Logger
	closed bool
}

// NewInmem returns an EventBus backed by in-memory channels. Logger may be nil (logging disabled).
func NewInmem(l logger.Logger) *Inmem {
	if l == nil {
		l = logger.NoopLogger()
	}
	return &Inmem{
		subs:   make(map[string][]chan []byte),
		logger: l,
	}
}

var _ EventBus = (*Inmem)(nil)

// Publish sends a copy of data to all subscribers of channel.
func (c *Inmem) Publish(ctx context.Context, channel string, data []byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	subs := append([]chan []byte(nil), c.subs[channel]...)
	c.mu.Unlock()

	c.logger.Debug(ctx, "eventbus publish", slog.String("channel", channel), slog.Int("payloadLen", len(data)))

	payload := append([]byte(nil), data...)
	for _, ch := range subs {
		select {
		case ch <- payload:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Subscribe registers a new subscriber for channel.
func (c *Inmem) Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClosed
	}
	c.logger.Debug(ctx, "eventbus subscribe", slog.String("channel", channel))
	ch := make(chan []byte, 64)
	c.subs[channel] = append(c.subs[channel], ch)
	c.mu.Unlock()

	closeFn := func() error {
		c.logger.Debug(context.Background(), "eventbus unsubscribe", slog.String("channel", channel))
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return nil
		}
		subs := c.subs[channel]
		for i, sub := range subs {
			if sub == ch {
				c.subs[channel] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
		return nil
	}
	return ch, closeFn, nil
}

// Close closes all subscriber channels and rejects further Publish/Subscribe.
// Idempotent; safe if individual closeFns run afterward.
func (c *Inmem) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.logger.Debug(context.Background(), "eventbus close")
	for _, subs := range c.subs {
		for _, ch := range subs {
			close(ch)
		}
	}
	c.subs = make(map[string][]chan []byte)
}
