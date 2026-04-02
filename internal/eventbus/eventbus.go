package eventbus

import "context"

// EventBus is pub/sub for agent events (streaming, approval fan-in).
// SDK runtimes use this internally; application code does not construct EventBus.
// Implementations may be in-memory, Redis-backed, or bridged from Temporal updates.
type EventBus interface {
	Publish(ctx context.Context, channel string, data []byte) error
	// Subscribe returns a receive-only channel of payloads and a close function. The caller must call close when done.
	Subscribe(ctx context.Context, channel string) (data <-chan []byte, closeFn func() error, err error)
}
