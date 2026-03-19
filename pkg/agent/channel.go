package agent

import (
	"context"
	"sync"

	"go.temporal.io/sdk/log"
	"go.uber.org/zap"
)

// agentChannel provides in-memory pub/sub for agent approval and events (same process only).
type agentChannel struct {
	mu     sync.Mutex
	subs   map[string][]chan []byte
	logger log.Logger
}

// newAgentChannel creates an in-memory channel implementation for the agent.
func newAgentChannel(logger log.Logger) *agentChannel {
	return &agentChannel{
		subs:   make(map[string][]chan []byte),
		logger: logger,
	}
}

// Publish sends data to all subscribers of the channel.
func (c *agentChannel) Publish(ctx context.Context, channel string, data []byte) error {
	c.mu.Lock()
	subs := append([]chan []byte(nil), c.subs[channel]...)
	c.mu.Unlock()

	c.logger.Debug("publishing to channel", zap.String("channel", channel), zap.Int("payloadLen", len(data)))

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

// Subscribe returns a channel that receives data.
func (c *agentChannel) Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error) {
	c.logger.Debug("subscribing to channel", zap.String("channel", channel))
	ch := make(chan []byte, 64)
	c.mu.Lock()
	c.subs[channel] = append(c.subs[channel], ch)
	c.mu.Unlock()

	closeFn := func() error {
		c.logger.Debug("closing channel", zap.String("channel", channel))
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
