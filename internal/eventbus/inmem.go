package eventbus

import (
	"context"
	"log/slog"
	"sync"

	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

// Inmem is a process-local pub/sub suitable for streaming and approval fan-in on one host.
type Inmem struct {
	mu     sync.Mutex
	subs   map[string][]chan []byte
	logger logger.Logger
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
	c.logger.Debug(ctx, "eventbus subscribe", slog.String("channel", channel))
	ch := make(chan []byte, 64)
	c.mu.Lock()
	c.subs[channel] = append(c.subs[channel], ch)
	c.mu.Unlock()

	closeFn := func() error {
		c.logger.Debug(context.Background(), "eventbus unsubscribe", slog.String("channel", channel))
		c.mu.Lock()
		subs := c.subs[channel]
		for i, sub := range subs {
			if sub == ch {
				c.subs[channel] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		close(ch)
		return nil
	}
	return ch, closeFn, nil
}
