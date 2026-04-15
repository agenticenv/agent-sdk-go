package gemini

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"google.golang.org/genai"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	cfg, err := llm.BuildConfig(llm.WithAPIKey("test-key"), llm.WithModel("gemini-2.0-flash"))
	if err != nil {
		t.Fatal(err)
	}
	return &Client{LLMConfig: *cfg}
}

func TestClient_Getters(t *testing.T) {
	c := testClient(t)
	if c.GetProvider() != interfaces.LLMProviderGemini {
		t.Fatal(c.GetProvider())
	}
	if c.GetModel() == "" {
		t.Fatal("model")
	}
	if !c.IsStreamSupported() {
		t.Fatal("stream")
	}
}

func TestGeminiThinkingLevelFromEffort(t *testing.T) {
	if geminiThinkingLevelFromEffort("") != "" {
		t.Fatal("empty")
	}
	if geminiThinkingLevelFromEffort("LOW") != genai.ThinkingLevelLow {
		t.Fatal("low")
	}
	if geminiThinkingLevelFromEffort("minimal") != genai.ThinkingLevelMinimal {
		t.Fatal("minimal")
	}
}

func TestGeminiThinkingConfig(t *testing.T) {
	if geminiThinkingConfig(nil) != nil {
		t.Fatal("nil reasoning")
	}
	if geminiThinkingConfig(&interfaces.LLMReasoning{}) != nil {
		t.Fatal("inactive")
	}
	tc := geminiThinkingConfig(&interfaces.LLMReasoning{BudgetTokens: 500})
	if tc == nil || tc.ThinkingBudget == nil || *tc.ThinkingBudget != 500 {
		t.Fatalf("%#v", tc)
	}
	tc = geminiThinkingConfig(&interfaces.LLMReasoning{Effort: "high"})
	if tc == nil || tc.ThinkingLevel != genai.ThinkingLevelHigh {
		t.Fatalf("%#v", tc)
	}
}

func TestBuildConfig_smoke(t *testing.T) {
	c := testClient(t)
	temp := float64(0.5)
	req := &interfaces.LLMRequest{
		SystemMessage: "sys",
		Messages:      []interfaces.Message{{Role: "user", Content: "hi"}},
		Temperature:   &temp,
		MaxTokens:     128,
		Tools: []interfaces.ToolSpec{{
			Name: "x", Description: "d",
			Parameters: interfaces.JSONSchema{"type": "object"},
		}},
		ResponseFormat: &interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON, Schema: map[string]any{"type": "object"}},
		Reasoning:      &interfaces.LLMReasoning{Effort: "medium"},
	}
	cfg := c.buildConfig(req)
	if cfg.MaxOutputTokens == 0 || cfg.Tools == nil {
		t.Fatalf("%#v", cfg)
	}
}

func TestApplyResponseFormat(t *testing.T) {
	cfg := &genai.GenerateContentConfig{}
	applyResponseFormat(cfg, &interfaces.ResponseFormat{Type: interfaces.ResponseFormatText})
	if cfg.ResponseMIMEType != "" {
		t.Fatal("text default")
	}
	applyResponseFormat(cfg, &interfaces.ResponseFormat{
		Type:   interfaces.ResponseFormatJSON,
		Schema: map[string]any{"type": "object"},
	})
	if cfg.ResponseMIMEType != "application/json" || cfg.ResponseJsonSchema == nil {
		t.Fatalf("json: %#v", cfg)
	}
}

func TestParseToolResponse(t *testing.T) {
	m := parseToolResponse(`{"ok":true}`)
	if m["ok"] != true {
		t.Fatalf("%v", m)
	}
	m = parseToolResponse("plain")
	if m["result"] != "plain" {
		t.Fatalf("%v", m)
	}
}

func TestGeminiUsageMetadataToLLM(t *testing.T) {
	if geminiUsageMetadataToLLM(nil) != nil {
		t.Fatal("nil")
	}
	if geminiUsageMetadataToLLM(&genai.GenerateContentResponseUsageMetadata{}) != nil {
		t.Fatal("zeros")
	}
	u := geminiUsageMetadataToLLM(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 1, CandidatesTokenCount: 2, TotalTokenCount: 3,
		CachedContentTokenCount: 4, ThoughtsTokenCount: 5,
	})
	if u.TotalTokens != 3 || u.CachedPromptTokens != 4 || u.ReasoningTokens != 5 {
		t.Fatalf("%#v", u)
	}
}

func TestGeminiResponseText(t *testing.T) {
	if geminiResponseText(nil) != "" {
		t.Fatal("nil resp")
	}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "a", Thought: true},
				{Text: "b", Thought: false},
			}},
		}},
	}
	if geminiResponseText(resp) != "b" {
		t.Fatal(geminiResponseText(resp))
	}
}

func TestToolsToGemini(t *testing.T) {
	tools := toolsToGemini([]interfaces.ToolSpec{{
		Name: "fn", Description: "d",
		Parameters: interfaces.JSONSchema{"type": "object"},
	}})
	if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("%#v", tools)
	}
	if toolsToGemini(nil) != nil {
		t.Fatal("nil specs")
	}
}

func TestGeminiToolCallsToInterface(t *testing.T) {
	if geminiToolCallsToInterface(nil) != nil {
		t.Fatal("nil")
	}
	out := geminiToolCallsToInterface([]*genai.FunctionCall{
		nil,
		{Name: ""},
		{Name: "f", Args: map[string]any{"x": 1}},
	})
	if len(out) != 1 || out[0].ToolName != "f" {
		t.Fatalf("%#v", out)
	}
}

// Gemini's API rejects requests where a tool result has an empty function_response.name
// (INVALID_ARGUMENT: Name cannot be empty). messagesToGemini must always set a non-empty name.
func TestMessagesToGemini_toolFunctionResponseNamesNeverEmpty(t *testing.T) {
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
		for _, name := range functionResponseNames(messagesToGemini(req)) {
			if name != "add" {
				t.Fatalf("expected function response name 'add', got %q", name)
			}
		}
	})

	t.Run("empty tool name falls back so API is valid", func(t *testing.T) {
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
					ToolName:   "",
				},
			},
		}
		names := functionResponseNames(messagesToGemini(req))
		if len(names) != 1 {
			t.Fatalf("expected one function response, got %d", len(names))
		}
		if names[0] != "tool" {
			t.Fatalf("expected fallback name 'tool' when ToolName is empty, got %q", names[0])
		}
	})
}

func functionResponseNames(contents []*genai.Content) []string {
	var names []string
	for _, c := range contents {
		for _, p := range c.Parts {
			if p != nil && p.FunctionResponse != nil {
				names = append(names, p.FunctionResponse.Name)
			}
		}
	}
	return names
}

func TestNewClient_requiresAPIKey(t *testing.T) {
	_, err := NewClient(llm.WithModel("gemini-2.5-flash"))
	if err == nil {
		t.Fatal("expected error when APIKey is not set")
	}
}

func TestGenerate(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY or GOOGLE_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gemini-2.5-flash"))
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
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY or GOOGLE_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gemini-2.5-flash"))
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
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY or GOOGLE_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gemini-2.5-flash"))
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
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY or GOOGLE_API_KEY not set")
	}
	c, err := NewClient(llm.WithAPIKey(apiKey), llm.WithModel("gemini-2.5-flash"))
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
