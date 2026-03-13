package interfaces

import "context"

// PubSub provides publish/subscribe for agent approval and events.
// Implement with in-memory (same process) or Redis/Kafka (cross-process).
type PubSub interface {
	// Publish sends a message to all subscribers of the channel.
	Publish(ctx context.Context, channel string, message []byte) error

	// Subscribe returns a channel that receives messages. Call the returned closeFn when done.
	Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error)
}
