// Package redis provides Redis-backed conversation storage.
// Use when using remote workers so the worker process can access conversation data.
// Agent and worker use the same Redis config; conversation ID is passed at runtime via Run and workflow input.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"
)

const keyPrefix = "conversation"

type RedisConversation struct {
	client    *redis.Client
	ownClient bool // true when we create client; Close() will close it. False when user provides WithClient.
	maxSize   int
	ttl       time.Duration

	// set by options; used when creating client (ignored if WithClient)
	addr      string
	password  string
	db        int
	keyPrefix string
}

// Option configures RedisConversation.
type Option func(*RedisConversation)

// WithClient sets the Redis client. When set, addr/password/db are ignored. Close() will NOT close the client.
func WithClient(client *redis.Client) Option {
	return func(c *RedisConversation) { c.client = client; c.ownClient = false }
}

// WithAddr sets the Redis address (e.g. "localhost:6379").
func WithAddr(addr string) Option {
	return func(c *RedisConversation) { c.addr = addr }
}

// WithPassword sets the Redis password.
func WithPassword(password string) Option {
	return func(c *RedisConversation) { c.password = password }
}

// WithDB sets the Redis database number (default 0).
func WithDB(db int) Option {
	return func(c *RedisConversation) { c.db = db }
}

// WithTTL sets the Redis key TTL.
func WithTTL(ttl time.Duration) Option {
	return func(c *RedisConversation) { c.ttl = ttl }
}

// WithKeyPrefix sets the Redis key prefix.
func WithKeyPrefix(prefix string) Option {
	return func(c *RedisConversation) { c.keyPrefix = prefix }
}

// WithMaxSize sets the maximum number of messages to store. Oldest are trimmed when exceeded.
func WithMaxSize(size int) Option {
	return func(c *RedisConversation) { c.maxSize = size }
}

func (c *RedisConversation) getKey(id string) string {
	p := c.keyPrefix
	if p == "" {
		p = keyPrefix
	}
	return fmt.Sprintf("%s:%s:messages", p, id)
}

// NewRedisConversation creates a Redis-backed conversation from options.
// Use WithClient to provide your own client; otherwise addr is required to create a client.
// Call Close() when done if you did not use WithClient.
func NewRedisConversation(opts ...Option) (*RedisConversation, error) {
	c := &RedisConversation{maxSize: 100}
	for _, opt := range opts {
		opt(c)
	}
	if c.maxSize <= 0 {
		c.maxSize = 100
	}
	if c.keyPrefix == "" {
		c.keyPrefix = keyPrefix
	}
	if c.ttl <= 0 {
		c.ttl = 24 * time.Hour
	}
	if c.client == nil {
		if c.addr == "" {
			return nil, errors.New("addr is required when not using WithClient")
		}
		c.client = redis.NewClient(&redis.Options{
			Addr:     c.addr,
			Password: c.password,
			DB:       c.db,
		})
		c.ownClient = true
	}
	return c, nil
}

// Close releases the Redis connection only when we own it (not using WithClient).
func (c *RedisConversation) Close() error {
	if c.ownClient && c.client != nil {
		return c.client.Close()
	}
	return nil
}

func (c *RedisConversation) IsDistributed() bool {
	return true
}

func (c *RedisConversation) AddMessage(ctx context.Context, id string, msg interfaces.Message) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	key := c.getKey(id)
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	pipe := c.client.Pipeline()
	pipe.RPush(ctx, key, string(data))
	llen := pipe.LLen(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis rpush: %w", err)
	}
	n, err := llen.Result()
	if err != nil {
		return fmt.Errorf("redis llen: %w", err)
	}
	if c.maxSize > 0 && n > int64(c.maxSize) {
		trim := n - int64(c.maxSize)
		if err := c.client.LTrim(ctx, key, trim, -1).Err(); err != nil {
			return fmt.Errorf("redis ltrim: %w", err)
		}
	}
	if c.ttl > 0 {
		if err := c.client.Expire(ctx, key, c.ttl).Err(); err != nil {
			return fmt.Errorf("redis expire: %w", err)
		}
	}
	return nil
}

func (c *RedisConversation) ListMessages(ctx context.Context, id string, opts ...interfaces.ListMessagesOption) ([]interfaces.Message, error) {
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	key := c.getKey(id)
	n, err := c.client.LLen(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis llen: %w", err)
	}
	lmo := &interfaces.ListMessagesOptions{
		Limit:  -1,
		Offset: -1,
		Roles: []interfaces.MessageRole{
			interfaces.MessageRoleUser,
			interfaces.MessageRoleAssistant,
			interfaces.MessageRoleTool,
		},
	}
	for _, opt := range opts {
		opt(lmo)
	}
	if lmo.Limit < 0 {
		lmo.Limit = int(n)
	}
	if lmo.Offset < 0 {
		lmo.Offset = 0
	}
	end := int(n) - lmo.Offset
	start := end - lmo.Limit
	if start < 0 {
		start = 0
	}
	if end > int(n) {
		end = int(n)
	}
	if start >= end {
		return []interfaces.Message{}, nil
	}
	vals, err := c.client.LRange(ctx, key, int64(start), int64(end-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis lrange: %w", err)
	}
	out := make([]interfaces.Message, 0, len(vals))
	for _, s := range vals {
		var msg interfaces.Message
		if err := json.Unmarshal([]byte(s), &msg); err != nil {
			return nil, fmt.Errorf("unmarshal message: %w", err)
		}
		if len(lmo.Roles) > 0 && !roleIn(msg.Role, lmo.Roles) {
			continue
		}
		out = append(out, msg)
	}
	return out, nil
}

func (c *RedisConversation) Clear(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	key := c.getKey(id)
	if err := c.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}

func roleIn(role interfaces.MessageRole, roles []interfaces.MessageRole) bool {
	for _, r := range roles {
		if role == r {
			return true
		}
	}
	return false
}
