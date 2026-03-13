package messaging

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
)

// Ensure Redis implements PubSub.
var _ interfaces.PubSub = (*Redis)(nil)

// Redis provides Redis-backed pub/sub for cross-process messaging (agent + worker).
type Redis struct {
	client *redis.Client
}

// NewRedis creates a Redis PubSub implementation. Addr format: "host:port" (e.g. "localhost:6379").
func NewRedis(addr string) (*Redis, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &Redis{client: client}, nil
}

// NewRedisWithOptions creates a Redis PubSub with custom options.
func NewRedisWithOptions(opts *redis.Options) (*Redis, error) {
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &Redis{client: client}, nil
}

// Publish sends a message to all subscribers of the channel.
func (r *Redis) Publish(ctx context.Context, channel string, message []byte) error {
	return r.client.Publish(ctx, channel, message).Err()
}

// Subscribe returns a channel that receives messages. Call the returned closeFn when done.
func (r *Redis) Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error) {
	pubsub := r.client.Subscribe(ctx, channel)
	if err := pubsub.Ping(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, fmt.Errorf("redis subscribe: %w", err)
	}

	ch := make(chan []byte, 64)
	var once sync.Once
	closeFn := func() error {
		var err error
		once.Do(func() {
			err = pubsub.Close()
		})
		return err
	}

	go func() {
		defer close(ch)
		redisCh := pubsub.Channel()
		for {
			select {
			case msg, ok := <-redisCh:
				if !ok {
					return
				}
				payload := []byte(msg.Payload)
				select {
				case ch <- payload:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				_ = pubsub.Close()
				return
			}
		}
	}()

	return ch, closeFn, nil
}
