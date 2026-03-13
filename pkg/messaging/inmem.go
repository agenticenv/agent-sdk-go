package messaging

import (
	"context"
	"sync"

	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
)

// Ensure InMemory implements PubSub.
var _ interfaces.PubSub = (*InMemory)(nil)

// InMemory provides in-memory pub/sub for agent approval and events (same process only).
type InMemory struct {
	mu   sync.Mutex
	subs map[string][]chan []byte
}

// NewInMemory creates an in-memory PubSub implementation.
func NewInMemory() *InMemory {
	return &InMemory{
		subs: make(map[string][]chan []byte),
	}
}

// Publish sends a message to all subscribers of the channel.
func (m *InMemory) Publish(ctx context.Context, channel string, message []byte) error {
	m.mu.Lock()
	subs := append([]chan []byte(nil), m.subs[channel]...)
	m.mu.Unlock()

	msg := append([]byte(nil), message...)
	for _, ch := range subs {
		select {
		case ch <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Subscribe returns a channel that receives messages.
func (m *InMemory) Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error) {
	ch := make(chan []byte, 64)
	m.mu.Lock()
	m.subs[channel] = append(m.subs[channel], ch)
	m.mu.Unlock()

	closeFn := func() error {
		m.mu.Lock()
		subs := m.subs[channel]
		for i, c := range subs {
			if c == ch {
				m.subs[channel] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		close(ch)
		return nil
	}
	return ch, closeFn, nil
}
