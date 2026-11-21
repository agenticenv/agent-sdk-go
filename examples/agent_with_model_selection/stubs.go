package main

import (
	"context"
	"errors"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
)

var errNoStream = errors.New("stream not used")

// textLLM returns one fixed assistant message.
type textLLM struct {
	model   string
	content string
}

func (t textLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{Content: t.content}, nil
}
func (textLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errNoStream
}
func (t textLLM) GetModel() string {
	if t.model != "" {
		return t.model
	}
	return "stub"
}
func (textLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (textLLM) IsStreamSupported() bool             { return false }

// toolThenTextLLM asks for a tool on the first call, then returns text.
type toolThenTextLLM struct {
	model   string
	content string
	calls   int
}

func (t *toolThenTextLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	t.calls++
	if t.calls == 1 {
		return &interfaces.LLMResponse{
			ToolCalls: []*interfaces.ToolCall{{
				ToolCallID: "c1",
				ToolName:   "lookup",
				Args:       map[string]any{"q": "status"},
			}},
		}, nil
	}
	return &interfaces.LLMResponse{Content: t.content}, nil
}
func (*toolThenTextLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errNoStream
}
func (t *toolThenTextLLM) GetModel() string {
	if t.model != "" {
		return t.model
	}
	return "stub"
}
func (*toolThenTextLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (*toolThenTextLLM) IsStreamSupported() bool             { return false }

type lookupTool struct{}

func (lookupTool) Name() string        { return "lookup" }
func (lookupTool) DisplayName() string { return "Lookup" }
func (lookupTool) Description() string { return "Look up a short status string." }
func (lookupTool) Parameters() interfaces.JSONSchema {
	return tools.Params(map[string]interfaces.JSONSchema{
		"q": tools.ParamString("What to look up"),
	}, "q")
}
func (lookupTool) Execute(context.Context, map[string]any) (any, error) {
	return "status=ok", nil
}

func requestHasSensitive(req interfaces.LLMRequest) bool {
	for _, m := range req.Messages {
		if strings.Contains(strings.ToLower(m.Content), "ssn") {
			return true
		}
	}
	return false
}

func requestHasToolResult(req interfaces.LLMRequest) bool {
	for _, m := range req.Messages {
		if m.Role == interfaces.MessageRoleTool {
			return true
		}
	}
	return false
}
