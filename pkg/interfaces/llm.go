package interfaces

import (
	"context"
	"encoding/json"
)

//go:generate mockgen -destination=./mocks/mock_llm.go -package=mocks github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces LLMClient

type LLMProvider string

const (
	LLMProviderOpenAI    LLMProvider = "openai"
	LLMProviderAnthropic LLMProvider = "anthropic"
	LLMProviderGemini   LLMProvider = "gemini"
)

type LLMClient interface {
	Generate(ctx context.Context, request *LLMRequest) (*LLMResponse, error)
	GenerateStream(ctx context.Context, request *LLMRequest) (LLMStream, error)
	GetModel() string
	GetProvider() LLMProvider
	// IsStreamSupported returns true if the client supports streaming (e.g. OpenAI, Anthropic).
	IsStreamSupported() bool
}

// LLMStream yields partial content and optional thinking/tool-call chunks from a streaming LLM response.
type LLMStream interface {
	Next() bool
	Current() *LLMStreamChunk
	Err() error
	// GetResult returns the accumulated content and tool calls after streaming completes.
	// Call after the Next loop; returns nil if streaming failed or was not completed.
	GetResult() *LLMResponse
}

// LLMStreamChunk is a single chunk from a streaming LLM response.
type LLMStreamChunk struct {
	ContentDelta  string      // partial text content
	ThinkingDelta string      // Anthropic extended thinking (optional)
	ToolCalls     []*ToolCall // set on final chunk when tool calls are present
}

type LLMRequest struct {
	SystemMessage  string
	ResponseFormat *ResponseFormat
	Tools          []ToolSpec // Tool specs for the LLM to choose from
	// Messages is the conversation history. For first turn, use one user message.
	// For continuation after tool use: append assistant (with ToolCalls) + tool result messages.
	Messages []Message

	// Sampling (per-request; typically set from agent config). nil/0 = provider default.
	Temperature *float64 // 0-2 OpenAI, 0-1 Anthropic
	MaxTokens   int      // 0 = provider default
	TopP        *float64 // 0-1; OpenAI only
	TopK        *int     // Anthropic only
}

type LLMResponse struct {
	Content  string
	Metadata map[string]any
	// ToolCalls contains any tool invocations the LLM chose; empty when none.
	ToolCalls []*ToolCall
}

// ToolCall is the LLM's decision to invoke a tool.
type ToolCall struct {
	ToolCallID string         `json:"tool_call_id"` // from API; needed to match tool results
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args"`
}

type ResponseFormatType string

const (
	ResponseFormatJSON ResponseFormatType = "json"
	ResponseFormatText ResponseFormatType = "text"
)

type JSONSchema map[string]any

func (s JSONSchema) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any(s))
}

type ResponseFormat struct {
	Type   ResponseFormatType
	Name   string
	Schema JSONSchema
}
