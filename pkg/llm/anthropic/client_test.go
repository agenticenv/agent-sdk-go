package anthropic

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/anthropics/anthropic-sdk-go"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	cfg, err := llm.BuildConfig(llm.WithAPIKey("test-key"), llm.WithModel("claude-3-5-haiku-20241022"))
	if err != nil {
		t.Fatal(err)
	}
	return &Client{LLMConfig: *cfg}
}

func TestClient_Getters(t *testing.T) {
	c := testClient(t)
	if c.GetProvider() != interfaces.LLMProviderAnthropic {
		t.Fatal(c.GetProvider())
	}
	if c.GetModel() == "" {
		t.Fatal("model")
	}
	if !c.IsStreamSupported() {
		t.Fatal("stream")
	}
}

func TestAnthropicThinkingBudget(t *testing.T) {
	if _, ok := anthropicThinkingBudget(nil); ok {
		t.Fatal("nil")
	}
	b, ok := anthropicThinkingBudget(&interfaces.LLMReasoning{BudgetTokens: 500})
	if !ok || b != 1024 {
		t.Fatalf("budget < 1024 clamps: %d %v", b, ok)
	}
	b, ok = anthropicThinkingBudget(&interfaces.LLMReasoning{BudgetTokens: 2000})
	if !ok || b != 2000 {
		t.Fatalf("got %d", b)
	}
	b, ok = anthropicThinkingBudget(&interfaces.LLMReasoning{Enabled: true})
	if !ok || b != 1024 {
		t.Fatalf("enabled only: %d", b)
	}
}

func TestBuildMessageParams_smoke(t *testing.T) {
	c := testClient(t)
	temp := 0.5
	req := &interfaces.LLMRequest{
		SystemMessage: "sys",
		Messages:      []interfaces.Message{{Role: "user", Content: "hello"}},
		Temperature:   &temp,
		MaxTokens:     100,
		Tools: []interfaces.ToolSpec{{
			Name: "x", Description: "d",
			Parameters: interfaces.JSONSchema{"type": "object"},
		}},
		ResponseFormat: &interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON, Schema: map[string]any{"type": "object"}},
		Reasoning:      &interfaces.LLMReasoning{BudgetTokens: 2048},
	}
	msgs := messagesToAnthropic(req)
	p := c.buildMessageParams(msgs, req)
	if p.Model == "" || p.MaxTokens == 0 || len(p.Messages) == 0 {
		t.Fatalf("params: %+v", p)
	}
}

func TestResponseFormatToAnthropic(t *testing.T) {
	out := responseFormatToAnthropic(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatText})
	if out.Format.Type != "" {
		t.Fatalf("text should be empty OutputConfig: %+v", out)
	}
	out = responseFormatToAnthropic(&interfaces.ResponseFormat{
		Type:   interfaces.ResponseFormatJSON,
		Schema: interfaces.JSONSchema{"type": "object"},
	})
	if out.Format.Type == "" {
		t.Fatal("expected json schema format")
	}
}

func TestToolInputSchema(t *testing.T) {
	s := toolInputSchema(nil)
	if s.Properties == nil {
		t.Fatal("expected empty properties map")
	}
	s = toolInputSchema(interfaces.JSONSchema{
		"properties": map[string]any{"x": map[string]any{"type": "string"}},
		"required":   []any{"x"},
	})
	if len(s.Required) != 1 || s.Required[0] != "x" {
		t.Fatalf("required: %#v", s.Required)
	}
	s = toolInputSchema(interfaces.JSONSchema{
		"properties": map[string]any{"y": map[string]any{}},
		"required":   []string{"y"},
	})
	if len(s.Required) != 1 {
		t.Fatalf("required []string: %#v", s.Required)
	}
}

func TestToolsToAnthropic(t *testing.T) {
	out := toolsToAnthropic([]interfaces.ToolSpec{{
		Name:        "t1",
		Description: "d",
		Parameters:  interfaces.JSONSchema{"type": "object", "properties": map[string]any{}},
	}})
	if len(out) != 1 || out[0].OfTool == nil || out[0].OfTool.Name != "t1" {
		t.Fatalf("%#v", out)
	}
}

func TestExtractContentAndToolCalls(t *testing.T) {
	blocks := []anthropic.ContentBlockUnion{
		{Type: "text", Text: "hi"},
		{Type: "tool_use", ID: "call-1", Name: "fn", Input: json.RawMessage(`{"a":1}`)},
	}
	text, calls := extractContentAndToolCalls(blocks)
	if text != "hi" || len(calls) != 1 || calls[0].ToolName != "fn" {
		t.Fatalf("text=%q calls=%#v", text, calls)
	}
}

func TestAnthropicUsageToLLM(t *testing.T) {
	if anthropicUsageToLLM(anthropic.Usage{}) != nil {
		t.Fatal("zero usage -> nil")
	}
	u := anthropicUsageToLLM(anthropic.Usage{InputTokens: 3, OutputTokens: 4, CacheReadInputTokens: 2})
	if u == nil || u.TotalTokens != 7 || u.CachedPromptTokens != 2 {
		t.Fatalf("%#v", u)
	}
}

// Anthropic requires a non-empty name on tool_use blocks. messagesToAnthropic must
// always set a non-empty tool name on assistant tool_use content.
func TestMessagesToAnthropic_toolUseNamesNeverEmpty(t *testing.T) {
	t.Parallel()

	t.Run("preserves non-empty tool name", func(t *testing.T) {
		t.Parallel()
		req := &interfaces.LLMRequest{
			Messages: []interfaces.Message{
				{Role: interfaces.MessageRoleUser, Content: "add 1 and 2"},
				{
					Role:    interfaces.MessageRoleAssistant,
					Content: "",
					ToolCalls: []*interfaces.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "add",
						Args:       map[string]any{"a": 1.0, "b": 2.0},
					}},
				},
				{
					Role:       interfaces.MessageRoleTool,
					Content:    `{"result":3}`,
					ToolCallID: "call-1",
					ToolName:   "add",
				},
			},
		}
		for _, name := range toolUseNamesFromMessages(messagesToAnthropic(req)) {
			if name != "add" {
				t.Fatalf("expected tool_use name 'add', got %q", name)
			}
		}
	})

	t.Run("empty tool name falls back so request is valid", func(t *testing.T) {
		t.Parallel()
		req := &interfaces.LLMRequest{
			Messages: []interfaces.Message{
				{Role: interfaces.MessageRoleUser, Content: "add 1 and 2"},
				{
					Role:    interfaces.MessageRoleAssistant,
					Content: "",
					ToolCalls: []*interfaces.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "",
						Args:       map[string]any{"a": 1.0, "b": 2.0},
					}},
				},
				{
					Role:       interfaces.MessageRoleTool,
					Content:    `{"result":3}`,
					ToolCallID: "call-1",
					ToolName:   "",
				},
			},
		}
		names := toolUseNamesFromMessages(messagesToAnthropic(req))
		if len(names) != 1 {
			t.Fatalf("expected one tool_use block, got %d", len(names))
		}
		if names[0] != "tool" {
			t.Fatalf("expected fallback name 'tool' when ToolName is empty, got %q", names[0])
		}
	})
}

func toolUseNamesFromMessages(msgs []anthropic.MessageParam) []string {
	var names []string
	for _, m := range msgs {
		if m.Role != anthropic.MessageParamRoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if b.OfToolUse != nil {
				names = append(names, b.OfToolUse.Name)
			}
		}
	}
	return names
}

func TestNewClient_requiresAPIKey(t *testing.T) {
	_, err := NewClient(llm.WithModel("claude-haiku-4-5"))
	if err == nil {
		t.Fatal("expected error when APIKey is not set")
	}
}

func TestGenerate(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("claude-haiku-4-5"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	req := &interfaces.LLMRequest{
		SystemMessage: "You are a helpful assistant. Reply briefly.",
		Messages:      []interfaces.Message{{Role: "user", Content: "Say hello in one word."}},
	}
	resp, err := c.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp == nil || resp.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestGenerate_ResponseFormatJSON(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("claude-haiku-4-5"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	req := &interfaces.LLMRequest{
		SystemMessage: "You are a helpful assistant. Reply with valid JSON only.",
		ResponseFormat: &interfaces.ResponseFormat{
			Type: interfaces.ResponseFormatJSON,
			Name: "Response",
			Schema: interfaces.JSONSchema{
				"type":                 "object",
				"properties":           interfaces.JSONSchema{"response": interfaces.JSONSchema{"type": "string"}},
				"required":             []any{"response"},
				"additionalProperties": false,
			},
		},
		Messages: []interfaces.Message{{Role: "user", Content: "Say hello in one word as JSON with key 'response'."}},
	}
	resp, err := c.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp == nil || resp.Content == "" {
		t.Fatal("expected non-empty content")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(resp.Content), &parsed); err != nil {
		t.Errorf("expected valid JSON response, got %q: %v", resp.Content, err)
	}
	if _, ok := parsed["response"]; !ok {
		t.Errorf("expected response with 'response' key, got %v", parsed)
	}
}

func TestGenerate_WithTools(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("claude-haiku-4-5"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	req := &interfaces.LLMRequest{
		SystemMessage: "You are a helpful assistant. Use the add tool when asked to add numbers.",
		Tools: []interfaces.ToolSpec{
			{
				Name:        "add",
				Description: "Add two numbers. Use when the user asks to add or sum numbers.",
				Parameters: interfaces.JSONSchema{
					"type": "object",
					"properties": interfaces.JSONSchema{
						"a": interfaces.JSONSchema{"type": "number", "description": "First number"},
						"b": interfaces.JSONSchema{"type": "number", "description": "Second number"},
					},
					"required": []any{"a", "b"},
				},
			},
		},
		Messages: []interfaces.Message{{Role: "user", Content: "What is 3 + 5? Use the add tool."}},
	}
	resp, err := c.Generate(ctx, req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.ToolCalls) == 0 {
		t.Error("expected at least one tool call, got none")
	}
	found := false
	for _, tc := range resp.ToolCalls {
		if tc != nil && tc.ToolName == "add" {
			found = true
			if tc.Args == nil {
				t.Error("expected tool call args")
			}
			break
		}
	}
	if !found {
		t.Errorf("expected tool call for 'add', got %v", resp.ToolCalls)
	}
}

func TestGenerateStream(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("claude-haiku-4-5"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	req := &interfaces.LLMRequest{
		SystemMessage: "You are a helpful assistant. Reply briefly.",
		Messages:      []interfaces.Message{{Role: "user", Content: "Say hello in one word."}},
	}
	stream, err := c.GenerateStream(ctx, req)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream")
	}
	for stream.Next() {
		_ = stream.Current()
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	result := stream.GetResult()
	if result == nil || result.Content == "" {
		t.Error("expected non-empty content from GetResult")
	}
}
