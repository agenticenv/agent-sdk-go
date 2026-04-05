package interfaces

import (
	"context"
	"encoding/json"
)

//go:generate mockgen -destination=./mocks/mock_llm.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/interfaces LLMClient

type LLMProvider string

const (
	LLMProviderOpenAI    LLMProvider = "openai"
	LLMProviderAnthropic LLMProvider = "anthropic"
	LLMProviderGemini    LLMProvider = "gemini"
)

type LLMClient interface {
	// Generate generates a response from the LLM.
	Generate(ctx context.Context, request *LLMRequest) (*LLMResponse, error)
	// GenerateStream generates a response from the LLM using streaming.
	GenerateStream(ctx context.Context, request *LLMRequest) (LLMStream, error)
	// GetModel returns the model name.
	GetModel() string
	// GetProvider returns the provider name.
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
	Temperature *float64 // 0-2 OpenAI, 0-1 Anthropic; also Gemini
	MaxTokens   int      // 0 = provider default
	TopP        *float64 // 0-1; OpenAI and Gemini (Anthropic client does not set TopP)
	TopK        *int     // Anthropic only

	// Reasoning configures generic reasoning/thinking when non-nil; each LLM client maps fields to its API.
	Reasoning *LLMReasoning
}

// LLMReasoning configures reasoning/thinking in a provider-agnostic way.
// Each LLM client maps these fields to its API; fields that do not apply are ignored.
type LLMReasoning struct {
	// Enabled requests reasoning/thinking where the provider supports it.
	// Anthropic: if true and BudgetTokens is 0, uses the minimum extended-thinking budget (1024 tokens).
	// OpenAI: does not infer reasoning_effort from Enabled alone (standard models reject that param).
	// Gemini: contributes to turning on thought output with IncludeThoughts.
	Enabled bool

	// Effort is a generic reasoning intensity: "none", "minimal", "low", "medium", "high", "xhigh".
	// OpenAI: sent as reasoning_effort only when non-empty; use only with reasoning-capable models.
	// Gemini: mapped to ThinkingLevel when recognized (low/medium/high/minimal), unless BudgetTokens > 0.
	// Anthropic: not used (use Enabled and BudgetTokens for extended thinking).
	Effort string

	// BudgetTokens is the token budget for internal reasoning / extended thinking.
	// Anthropic: extended thinking; must be >= 1024 when non-zero (values below are clamped).
	// Gemini: ThinkingBudget. If non-zero, Effort is not mapped to ThinkingLevel (API allows only one).
	// OpenAI: not used.
	BudgetTokens int
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
