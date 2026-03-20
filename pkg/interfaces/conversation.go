package interfaces

import (
	"context"
	"time"
)

//go:generate mockgen -destination=./mocks/mock_conversation.go -package=mocks github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces Conversation

type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

// Message represents a conversation turn for multi-turn (including tool use).
type Message struct {
	Role    MessageRole `json:"role"`
	Content string      `json:"content"`

	ToolName   string      `json:"tool_name"`
	ToolCallID string      `json:"tool_call_id"`
	ToolCalls  []*ToolCall `json:"tool_calls"`

	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"created_at"`
}

type Conversation interface {
	// AddMessage adds a message to the conversation identified by id. Id is passed at runtime (e.g. from Run input, workflow).
	AddMessage(ctx context.Context, id string, msg Message) error

	// ListMessages returns messages for the conversation identified by id.
	ListMessages(ctx context.Context, id string, opts ...ListMessagesOption) ([]Message, error)

	// Clear removes all messages for the conversation identified by id. Called by the user when ending a session.
	Clear(ctx context.Context, id string) error

	// IsDistributed returns true if the implementation uses distributed storage (Redis, Postgres, etc.).
	// In-memory implementations return false. Use distributed implementations when using remote workers.
	IsDistributed() bool
}

type ListMessagesOptions struct {
	// Limit is the maximum number of messages to retrieve from recent. -1 = all.
	Limit int

	// Offset is the number of most recent messages to skip. -1 = 0 (default).
	Offset int

	// Roles filters messages by role
	Roles []MessageRole
}

type ListMessagesOption func(*ListMessagesOptions)

// WithLimit sets the maximum number of messages to retrieve
func WithLimit(limit int) ListMessagesOption {
	return func(o *ListMessagesOptions) {
		o.Limit = limit
	}
}

// WithOffset sets the number of messages to skip
func WithOffset(offset int) ListMessagesOption {
	return func(o *ListMessagesOptions) {
		o.Offset = offset
	}
}

// WithRoles filters messages by role
func WithRoles(roles ...MessageRole) ListMessagesOption {
	return func(o *ListMessagesOptions) {
		o.Roles = roles
	}
}
