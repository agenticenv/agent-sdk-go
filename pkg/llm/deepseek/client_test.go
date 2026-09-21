package deepseek

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/openai/openai-go/v3"
)

// compile-time check mirrors the assertion in client.go.
var _ interfaces.LLMClient = (*Client)(nil)

func newTestClient(t *testing.T, opts ...llm.Option) *Client {
	t.Helper()
	c, err := NewClient(append([]llm.Option{llm.WithAPIKey("test-key")}, opts...)...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestClient_Getters(t *testing.T) {
	c := newTestClient(t, llm.WithModel("deepseek-reasoner"))
	if c.GetProvider() != interfaces.LLMProviderDeepSeek {
		t.Fatalf("GetProvider() = %q, want deepseek", c.GetProvider())
	}
	if c.GetModel() != "deepseek-reasoner" {
		t.Fatalf("GetModel() = %q", c.GetModel())
	}
	if !c.IsStreamSupported() {
		t.Fatal("IsStreamSupported() = false, want true")
	}
}

func TestNewClient_RequiresAPIKey(t *testing.T) {
	if _, err := NewClient(llm.WithModel("deepseek-chat")); err == nil {
		t.Fatal("NewClient without APIKey: expected error, got nil")
	}
}

func TestNewClient_DefaultBaseURL(t *testing.T) {
	c := newTestClient(t)
	if c.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want default %q", c.BaseURL, DefaultBaseURL)
	}
}

func TestNewClient_BaseURLOverride(t *testing.T) {
	const custom = "https://proxy.example.com/v1"
	c := newTestClient(t, llm.WithBaseURL(custom))
	if c.BaseURL != custom {
		t.Fatalf("BaseURL = %q, want override %q", c.BaseURL, custom)
	}
}

func TestExtractReasoning(t *testing.T) {
	// nil map -> "".
	if got := extractReasoning(nil); got != "" {
		t.Fatalf("nil -> %q", got)
	}
	// Missing reasoning_content -> "" (e.g. deepseek-chat).
	var msg openai.ChatCompletionMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"hi"}`), &msg); err != nil {
		t.Fatal(err)
	}
	if got := extractReasoning(msg.JSON.ExtraFields); got != "" {
		t.Fatalf("missing -> %q", got)
	}
	// Present reasoning_content (deepseek-reasoner) -> decoded string.
	var rmsg openai.ChatCompletionMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"4","reasoning_content":"2+2=4"}`), &rmsg); err != nil {
		t.Fatal(err)
	}
	if got := extractReasoning(rmsg.JSON.ExtraFields); got != "2+2=4" {
		t.Fatalf("present -> %q", got)
	}
}

func TestDeepSeekResponseToLLM(t *testing.T) {
	resp := &openai.ChatCompletion{
		Model: "deepseek-chat",
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
	out := deepSeekResponseToLLM(resp)
	if out.Content != "hello" || len(out.ToolCalls) != 1 || out.ToolCalls[0].ToolName != "fn" {
		t.Fatalf("%#v", out)
	}
	if out.ToolCalls[0].Args["x"] != float64(1) {
		t.Fatalf("args = %#v", out.ToolCalls[0].Args)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 3 {
		t.Fatalf("usage %#v", out.Usage)
	}
	if _, ok := out.Metadata[reasoningContentField]; ok {
		t.Fatal("no reasoning expected for deepseek-chat response")
	}
}

func TestDeepSeekResponseToLLM_Reasoning(t *testing.T) {
	var msg openai.ChatCompletionMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"4","reasoning_content":"2+2"}`), &msg); err != nil {
		t.Fatal(err)
	}
	resp := &openai.ChatCompletion{
		Model:   "deepseek-reasoner",
		Choices: []openai.ChatCompletionChoice{{Message: msg}},
	}
	out := deepSeekResponseToLLM(resp)
	if out.Metadata[reasoningContentField] != "2+2" {
		t.Fatalf("metadata reasoning = %v", out.Metadata[reasoningContentField])
	}
}

func TestDeepSeekUsageToLLM(t *testing.T) {
	if deepSeekUsageToLLM(openai.CompletionUsage{}) != nil {
		t.Fatal("zero usage -> nil")
	}
	u := openai.CompletionUsage{
		PromptTokens:            10,
		CompletionTokens:        20,
		TotalTokens:             30,
		PromptTokensDetails:     openai.CompletionUsagePromptTokensDetails{CachedTokens: 5},
		CompletionTokensDetails: openai.CompletionUsageCompletionTokensDetails{ReasoningTokens: 3},
	}
	out := deepSeekUsageToLLM(u)
	if out == nil || out.CachedPromptTokens != 5 || out.ReasoningTokens != 3 || out.TotalTokens != 30 {
		t.Fatalf("%#v", out)
	}
}

// messagesToDeepSeek must use the "system" role for the system prompt (DeepSeek rejects
// OpenAI's "developer" role) and translate user/assistant/tool turns.
func TestMessagesToDeepSeek_SystemRole(t *testing.T) {
	req := &interfaces.LLMRequest{
		SystemMessage: "be terse",
		Messages: []interfaces.Message{
			{Role: "user", Content: "hi"},
		},
	}
	msgs := messagesToDeepSeek(req)
	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2", len(msgs))
	}
	if msgs[0].OfSystem == nil {
		t.Fatalf("first message should use the system role, got %#v", msgs[0])
	}
	if msgs[1].OfUser == nil {
		t.Fatal("second message should be a user message")
	}
}

func TestMessagesToDeepSeek_AssistantToolCallsAndToolResult(t *testing.T) {
	req := &interfaces.LLMRequest{
		Messages: []interfaces.Message{
			{Role: "assistant", Content: "", ToolCalls: []*interfaces.ToolCall{
				{ToolCallID: "tc1", ToolName: "get_weather", Args: map[string]any{"city": "SF"}},
			}},
			{Role: "tool", Content: "sunny", ToolCallID: "tc1"},
		},
	}
	msgs := messagesToDeepSeek(req)
	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2", len(msgs))
	}
	if msgs[0].OfAssistant == nil || len(msgs[0].OfAssistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool calls not mapped: %#v", msgs[0])
	}
	if msgs[1].OfTool == nil {
		t.Fatalf("tool result not mapped: %#v", msgs[1])
	}
}

func TestToolsToDeepSeek(t *testing.T) {
	specs := []interfaces.ToolSpec{
		{Name: "search", Description: "search the web", Parameters: interfaces.JSONSchema{"type": "object"}},
		{Name: "noparams"}, // nil Parameters -> defaulted object schema
	}
	tools := toolsToDeepSeek(specs)
	if len(tools) != 2 {
		t.Fatalf("len = %d, want 2", len(tools))
	}
	if tools[0].OfFunction == nil || tools[0].OfFunction.Function.Name != "search" {
		t.Fatalf("tool 0 = %#v", tools[0])
	}
	if tools[1].OfFunction.Function.Parameters == nil {
		t.Fatal("nil parameters should be defaulted to an object schema")
	}
}

// DeepSeek supports only json_object: a JSON request maps to json_object with or without a
// Schema (the schema is dropped, since DeepSeek rejects OpenAI's json_schema type).
func TestResponseFormatToDeepSeek(t *testing.T) {
	// Text.
	if rf := responseFormatToDeepSeek(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatText}); rf.OfText == nil {
		t.Fatal("text -> OfText expected")
	}
	// JSON without schema -> json_object.
	if rf := responseFormatToDeepSeek(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON}); rf.OfJSONObject == nil {
		t.Fatal("json (no schema) -> OfJSONObject expected")
	}
	// JSON *with* schema -> still json_object (schema ignored), never json_schema.
	withSchema := &interfaces.ResponseFormat{
		Type:   interfaces.ResponseFormatJSON,
		Name:   "FactAnswer",
		Schema: interfaces.JSONSchema{"type": "object"},
	}
	rf := responseFormatToDeepSeek(withSchema)
	if rf.OfJSONObject == nil {
		t.Fatal("json (with schema) -> OfJSONObject expected")
	}
	if rf.OfJSONSchema != nil {
		t.Fatal("DeepSeek must not emit json_schema (unsupported)")
	}
}

func TestBuildCompletionParams(t *testing.T) {
	c := newTestClient(t, llm.WithModel("deepseek-chat"))
	temp, topP := 0.3, 0.9
	req := &interfaces.LLMRequest{
		Messages:       []interfaces.Message{{Role: "user", Content: "hi"}},
		Temperature:    &temp,
		TopP:           &topP,
		MaxTokens:      50,
		Tools:          []interfaces.ToolSpec{{Name: "fn"}},
		ResponseFormat: &interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON},
	}
	params := c.buildCompletionParams(messagesToDeepSeek(req), req)
	if params.Model != "deepseek-chat" {
		t.Fatalf("model = %q", params.Model)
	}
	if params.Temperature.Value != 0.3 {
		t.Fatalf("temperature = %v", params.Temperature.Value)
	}
	if params.TopP.Value != 0.9 {
		t.Fatalf("topP = %v", params.TopP.Value)
	}
	if params.MaxTokens.Value != 50 {
		t.Fatalf("maxTokens = %v", params.MaxTokens.Value)
	}
	if len(params.Tools) != 1 {
		t.Fatalf("tools = %d", len(params.Tools))
	}
	if params.ResponseFormat.OfJSONObject == nil {
		t.Fatal("response format should be json_object")
	}
}

func TestWrapLLM_DeepSeekError(t *testing.T) {
	err := wrapLLM(&openai.Error{StatusCode: 400, Message: "context_length_exceeded"})
	var llmErr *interfaces.LLMError
	if !errors.As(err, &llmErr) || llmErr.Reason != interfaces.LLMReasonContextExceeded {
		t.Fatalf("got %v", err)
	}
}
