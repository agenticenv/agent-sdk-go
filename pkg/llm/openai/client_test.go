package openai

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/openai/openai-go/v3"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	cfg, err := llm.BuildConfig(llm.WithAPIKey("test-key"), llm.WithModel("gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	return &Client{LLMConfig: *cfg}
}

func TestClient_Getters(t *testing.T) {
	c := testClient(t)
	if c.GetProvider() != interfaces.LLMProviderOpenAI {
		t.Fatal(c.GetProvider())
	}
	if c.GetModel() == "" {
		t.Fatal("model")
	}
	if !c.IsStreamSupported() {
		t.Fatal("stream")
	}
}

func TestOpenAIReasoningEffort(t *testing.T) {
	if _, ok := openAIReasoningEffort(nil); ok {
		t.Fatal("nil")
	}
	e, ok := openAIReasoningEffort(&interfaces.LLMReasoning{Effort: "  high  "})
	if !ok || string(e) != "high" {
		t.Fatalf("%v %v", e, ok)
	}
	if _, ok := openAIReasoningEffort(&interfaces.LLMReasoning{Enabled: true}); ok {
		t.Fatal("enabled alone should not set effort")
	}
}

func TestOpenAICompletionUsageToLLM(t *testing.T) {
	if openAICompletionUsageToLLM(openai.CompletionUsage{}) != nil {
		t.Fatal("zero -> nil")
	}
	u := openai.CompletionUsage{
		PromptTokens:            10,
		CompletionTokens:        20,
		TotalTokens:             30,
		PromptTokensDetails:     openai.CompletionUsagePromptTokensDetails{CachedTokens: 5},
		CompletionTokensDetails: openai.CompletionUsageCompletionTokensDetails{ReasoningTokens: 3},
	}
	out := openAICompletionUsageToLLM(u)
	if out == nil || out.CachedPromptTokens != 5 || out.ReasoningTokens != 3 {
		t.Fatalf("%#v", out)
	}
}

func TestOpenAIResponseToLLM(t *testing.T) {
	resp := &openai.ChatCompletion{
		Model: "gpt-4o-mini",
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{
				Content: "hello",
				ToolCalls: []openai.ChatCompletionMessageToolCallUnion{{
					ID:   "tc1",
					Type: "function",
					Function: openai.ChatCompletionMessageFunctionToolCallFunction{
						Name:      "fn",
						Arguments: `{"x":1}`,
					},
				}},
			},
		}},
		Usage: openai.CompletionUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}
	out := openAIResponseToLLM(resp)
	if out.Content != "hello" || len(out.ToolCalls) != 1 || out.ToolCalls[0].ToolName != "fn" {
		t.Fatalf("%#v", out)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 3 {
		t.Fatalf("usage %#v", out.Usage)
	}
}

func TestBuildCompletionParams_smoke(t *testing.T) {
	c := testClient(t)
	temp := 0.3
	req := &interfaces.LLMRequest{
		Messages:    []interfaces.Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		MaxTokens:   50,
		Tools: []interfaces.ToolSpec{{
			Name: "x", Description: "d",
			Parameters: interfaces.JSONSchema{"type": "object"},
		}},
		ResponseFormat: &interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON},
		Reasoning:      &interfaces.LLMReasoning{Effort: "low"},
	}
	msgs := messagesToOpenAI(req)
	p := c.buildCompletionParams(msgs, req)
	if p.Model == "" || len(p.Messages) == 0 {
		t.Fatal("empty params")
	}
}

func TestResponseFormatToOpenAI(t *testing.T) {
	u := responseFormatToOpenAI(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatText})
	if u.OfText == nil {
		t.Fatal("text")
	}
	u = responseFormatToOpenAI(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON})
	if u.OfJSONObject == nil {
		t.Fatal("json_object")
	}
	u = responseFormatToOpenAI(&interfaces.ResponseFormat{
		Type:   interfaces.ResponseFormatJSON,
		Schema: map[string]any{"type": "object"},
		Name:   "MySchema",
	})
	if u.OfJSONSchema == nil {
		t.Fatal("json_schema")
	}
}

func TestToolsToOpenAI(t *testing.T) {
	out := toolsToOpenAI([]interfaces.ToolSpec{{
		Name:        "t1",
		Description: "d",
		Parameters:  interfaces.JSONSchema{"type": "object", "properties": map[string]any{}},
	}})
	if len(out) != 1 || out[0].OfFunction == nil {
		t.Fatalf("%#v", out)
	}
}

// OpenAI requires a non-empty function name on assistant tool calls. messagesToOpenAI
// must never leave Function.Name empty (invalid request / provider errors).
func TestMessagesToOpenAI_assistantToolFunctionNamesNeverEmpty(t *testing.T) {
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
		for _, name := range assistantToolFunctionNames(messagesToOpenAI(req)) {
			if name != "add" {
				t.Fatalf("expected assistant tool function name 'add', got %q", name)
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
		names := assistantToolFunctionNames(messagesToOpenAI(req))
		if len(names) != 1 {
			t.Fatalf("expected one assistant tool call, got %d", len(names))
		}
		if names[0] != "tool" {
			t.Fatalf("expected fallback name 'tool' when ToolName is empty, got %q", names[0])
		}
	})
}

func assistantToolFunctionNames(messages []openai.ChatCompletionMessageParamUnion) []string {
	var names []string
	for _, m := range messages {
		for _, tc := range m.GetToolCalls() {
			if fn := tc.GetFunction(); fn != nil && fn.Name != "" {
				names = append(names, fn.Name)
			}
		}
	}
	return names
}

func TestNewClient_requiresAPIKey(t *testing.T) {
	_, err := NewClient(llm.WithModel("gpt-4o-mini"))
	if err == nil {
		t.Fatal("expected error when APIKey is not set")
	}
}

func TestGenerate(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gpt-4o-mini"))
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
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gpt-4o-mini"))
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
				"type":       "object",
				"properties": interfaces.JSONSchema{"response": interfaces.JSONSchema{"type": "string"}},
				"required":   []any{"response"},
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
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gpt-4o-mini"))
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
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gpt-4o-mini"))
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
