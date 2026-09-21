package main

import (
	"context"
	"errors"
	"sync"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
)

// failLLM always returns a classified rate-limit error.
type failLLM struct{}

func (failLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return nil, &interfaces.LLMError{
		Reason:     interfaces.LLMReasonRateLimit,
		StatusCode: 429,
		Err:        errors.New("quota"),
	}
}
func (failLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stream not used")
}
func (failLLM) GetModel() string                    { return "primary-stub" }
func (failLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (failLLM) IsStreamSupported() bool             { return false }

// textLLM returns a fixed assistant message.
type textLLM struct{ content string }

func (t textLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{Content: t.content}, nil
}
func (textLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stream not used")
}
func (t textLLM) GetModel() string                  { return "fallback-stub" }
func (textLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderAnthropic }
func (textLLM) IsStreamSupported() bool             { return false }

// seqLLM returns canned responses in order (tool calls, then text).
type seqLLM struct {
	mu        sync.Mutex
	responses []*interfaces.LLMResponse
	call      int
	model     string
}

func (s *seqLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.call
	s.call++
	if i < len(s.responses) {
		return s.responses[i], nil
	}
	return &interfaces.LLMResponse{Content: "done"}, nil
}
func (*seqLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stream not used")
}
func (s *seqLLM) GetModel() string {
	if s.model != "" {
		return s.model
	}
	return "seq-stub"
}
func (*seqLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (*seqLLM) IsStreamSupported() bool             { return false }

func toolCall(id, name string, args map[string]any) *interfaces.ToolCall {
	return &interfaces.ToolCall{ToolCallID: id, ToolName: name, Args: args}
}

// failTool always errors so the circuit breaker can trip.
type failTool struct {
	name string
	err  error
}

func (t failTool) Name() string        { return t.name }
func (t failTool) DisplayName() string { return t.name }
func (t failTool) Description() string {
	return "Demo tool that fails so error-control can extend iterations or trip the breaker."
}
func (failTool) Parameters() interfaces.JSONSchema {
	return tools.Params(map[string]interfaces.JSONSchema{
		"n": tools.ParamInteger("Call index"),
	}, "n")
}
func (t failTool) Execute(context.Context, map[string]any) (any, error) {
	if t.err != nil {
		return nil, t.err
	}
	return "ok", nil
}
