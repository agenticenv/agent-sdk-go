// Package inmem provides in-memory conversation storage.
// Use when the agent and worker run in the same process. For remote workers, use redis.
package inmem

import (
	"context"
	"sync"

	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
)

type InMemoryConversation struct {
	maxSize  int
	messages map[string][]interfaces.Message
	mu       sync.RWMutex
}

func NewInMemoryConversation(options ...Option) *InMemoryConversation {
	c := &InMemoryConversation{
		messages: make(map[string][]interfaces.Message),
		maxSize:  100,
	}
	for _, option := range options {
		option(c)
	}
	if c.maxSize <= 0 {
		c.maxSize = 100
	}
	return c
}

type Option func(*InMemoryConversation)

// WithMaxSize sets the maximum number of messages to store per conversation.
func WithMaxSize(size int) Option {
	return func(c *InMemoryConversation) { c.maxSize = size }
}

func (c *InMemoryConversation) IsDistributed() bool {
	return false
}

func (c *InMemoryConversation) AddMessage(ctx context.Context, id string, msg interfaces.Message) error {
	if id == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs := c.messages[id]
	msgs = append(msgs, msg)
	if c.maxSize > 0 && len(msgs) > c.maxSize {
		msgs = msgs[len(msgs)-c.maxSize:]
	}
	c.messages[id] = msgs
	return nil
}

func (c *InMemoryConversation) ListMessages(ctx context.Context, id string, opts ...interfaces.ListMessagesOption) ([]interfaces.Message, error) {
	if id == "" {
		return []interfaces.Message{}, nil
	}
	c.mu.RLock()
	msgs := c.messages[id]
	if msgs == nil {
		c.mu.RUnlock()
		return []interfaces.Message{}, nil
	}
	// copy to avoid holding lock during filter
	cp := make([]interfaces.Message, len(msgs))
	copy(cp, msgs)
	c.mu.RUnlock()

	n := len(cp)
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
		lmo.Limit = n
	}
	if lmo.Offset < 0 {
		lmo.Offset = 0
	}
	end := n - lmo.Offset
	start := end - lmo.Limit
	if start < 0 {
		start = 0
	}
	if end > n {
		end = n
	}
	out := make([]interfaces.Message, 0, end-start)
	for i := start; i < end; i++ {
		msg := cp[i]
		if len(lmo.Roles) > 0 && !roleIn(msg.Role, lmo.Roles) {
			continue
		}
		out = append(out, msg)
	}
	return out, nil
}

func roleIn(role interfaces.MessageRole, roles []interfaces.MessageRole) bool {
	for _, r := range roles {
		if role == r {
			return true
		}
	}
	return false
}

func (c *InMemoryConversation) Clear(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.messages, id)
	return nil
}
