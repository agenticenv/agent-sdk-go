package ollama

import (
	"encoding/json"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/openai/openai-go/v3"
)

// compile-time check mirrors the assertion in client.go.
var _ interfaces.LLMClient = (*Client)(nil)

func newTestClient(t *testing.T, opts ...llm.Option) *Client {
	t.Helper()
	// Isolate from the environment: an OLLAMA_API_KEY set on the host/CI must not silently
	// change local-default tests to the cloud default. Cloud-specific tests set it back.
	t.Setenv(cloudAPIKeyEnvVar, "")
	c, err := NewClient(opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestClient_Getters(t *testing.T) {
	c := newTestClient(t, llm.WithModel("llama3.2"))
	if c.GetProvider() != interfaces.LLMProviderOllama {
		t.Fatalf("GetProvider() = %q, want ollama", c.GetProvider())
	}
	if c.GetModel() != "llama3.2" {
		t.Fatalf("GetModel() = %q", c.GetModel())
	}
	if !c.IsStreamSupported() {
		t.Fatal("IsStreamSupported() = false, want true")
	}
}

// Unlike the hosted providers, Ollama needs no API key: NewClient must succeed without one.
func TestNewClient_NoAPIKey(t *testing.T) {
	if _, err := NewClient(llm.WithModel("llama3.2")); err != nil {
		t.Fatalf("NewClient without APIKey: expected success, got %v", err)
	}
}

func TestNewClient_DefaultBaseURL(t *testing.T) {
	c := newTestClient(t)
	if c.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want default %q", c.BaseURL, DefaultBaseURL)
	}
}

func TestNewClient_BaseURLOverride(t *testing.T) {
	const custom = "http://remote-host:11434/v1"
	c := newTestClient(t, llm.WithBaseURL(custom))
	if c.BaseURL != custom {
		t.Fatalf("BaseURL = %q, want override %q", c.BaseURL, custom)
	}
}

// Supplying an API key (no explicit base URL) switches the default to Ollama Cloud.
func TestNewClient_CloudBaseURL_WithAPIKeyOption(t *testing.T) {
	c := newTestClient(t, llm.WithAPIKey("cloud-key"), llm.WithModel("qwen3-coder:480b-cloud"))
	if c.BaseURL != DefaultCloudBaseURL {
		t.Fatalf("BaseURL = %q, want cloud default %q", c.BaseURL, DefaultCloudBaseURL)
	}
	if c.APIKey != "cloud-key" {
		t.Fatalf("APIKey = %q, want %q", c.APIKey, "cloud-key")
	}
}

// OLLAMA_API_KEY is a fallback when llm.WithAPIKey is not supplied, matching the Ollama CLI's
// own convention, and it also switches the default base URL to Ollama Cloud.
func TestNewClient_CloudBaseURL_FromEnvVar(t *testing.T) {
	t.Setenv(cloudAPIKeyEnvVar, "env-key")
	c, err := NewClient(llm.WithModel("llama3.2"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.BaseURL != DefaultCloudBaseURL {
		t.Fatalf("BaseURL = %q, want cloud default %q", c.BaseURL, DefaultCloudBaseURL)
	}
	if c.APIKey != "env-key" {
		t.Fatalf("APIKey = %q, want %q", c.APIKey, "env-key")
	}
}

// llm.WithAPIKey always takes precedence over OLLAMA_API_KEY, mirroring the Gemini client's
// WithAPIKey/GOOGLE_API_KEY precedence.
func TestNewClient_APIKeyOptionTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv(cloudAPIKeyEnvVar, "env-key")
	c, err := NewClient(llm.WithAPIKey("explicit-key"), llm.WithModel("llama3.2"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.APIKey != "explicit-key" {
		t.Fatalf("APIKey = %q, want %q", c.APIKey, "explicit-key")
	}
}

// An explicit base URL always wins over the cloud default, e.g. a self-hosted daemon behind
// an auth proxy that still needs a real API key.
func TestNewClient_ExplicitBaseURLOverridesCloudDefault(t *testing.T) {
	const custom = "https://proxy.example.com/v1"
	c := newTestClient(t, llm.WithAPIKey("cloud-key"), llm.WithBaseURL(custom))
	if c.BaseURL != custom {
		t.Fatalf("BaseURL = %q, want override %q", c.BaseURL, custom)
	}
}

func TestExtractReasoning(t *testing.T) {
	// nil map -> "".
	if got := extractReasoning(nil); got != "" {
		t.Fatalf("nil -> %q", got)
	}
	// Missing reasoning_content -> "" (e.g. llama3.2).
	var msg openai.ChatCompletionMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"hi"}`), &msg); err != nil {
		t.Fatal(err)
	}
	if got := extractReasoning(msg.JSON.ExtraFields); got != "" {
		t.Fatalf("missing -> %q", got)
	}
	// Present reasoning_content (e.g. deepseek-r1 via Ollama) -> decoded string.
	var rmsg openai.ChatCompletionMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"4","reasoning_content":"2+2=4"}`), &rmsg); err != nil {
		t.Fatal(err)
	}
	if got := extractReasoning(rmsg.JSON.ExtraFields); got != "2+2=4" {
		t.Fatalf("present -> %q", got)
	}
}

func TestOllamaResponseToLLM(t *testing.T) {
	resp := &openai.ChatCompletion{
		Model: "llama3.2",
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
	out := ollamaResponseToLLM(resp)
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
		t.Fatal("no reasoning expected for a non-thinking response")
	}
}

func TestOllamaResponseToLLM_Reasoning(t *testing.T) {
	var msg openai.ChatCompletionMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"4","reasoning_content":"2+2"}`), &msg); err != nil {
		t.Fatal(err)
	}
	resp := &openai.ChatCompletion{
		Model:   "deepseek-r1",
		Choices: []openai.ChatCompletionChoice{{Message: msg}},
	}
	out := ollamaResponseToLLM(resp)
	if out.Metadata[reasoningContentField] != "2+2" {
		t.Fatalf("metadata reasoning = %v", out.Metadata[reasoningContentField])
	}
}

func TestOllamaUsageToLLM(t *testing.T) {
	if ollamaUsageToLLM(openai.CompletionUsage{}) != nil {
		t.Fatal("zero usage -> nil")
	}
	u := openai.CompletionUsage{
		PromptTokens:            10,
		CompletionTokens:        20,
		TotalTokens:             30,
		PromptTokensDetails:     openai.CompletionUsagePromptTokensDetails{CachedTokens: 5},
		CompletionTokensDetails: openai.CompletionUsageCompletionTokensDetails{ReasoningTokens: 3},
	}
	out := ollamaUsageToLLM(u)
	if out == nil || out.CachedPromptTokens != 5 || out.ReasoningTokens != 3 || out.TotalTokens != 30 {
		t.Fatalf("%#v", out)
	}
}

// messagesToOllama must use the "system" role for the system prompt and translate
// user/assistant/tool turns.
func TestMessagesToOllama_SystemRole(t *testing.T) {
	req := &interfaces.LLMRequest{
		SystemMessage: "be terse",
		Messages: []interfaces.Message{
			{Role: "user", Content: "hi"},
		},
	}
	msgs := messagesToOllama(req)
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

func TestMessagesToOllama_AssistantToolCallsAndToolResult(t *testing.T) {
	req := &interfaces.LLMRequest{
		Messages: []interfaces.Message{
			{Role: "assistant", Content: "", ToolCalls: []*interfaces.ToolCall{
				{ToolCallID: "tc1", ToolName: "get_weather", Args: map[string]any{"city": "SF"}},
			}},
			{Role: "tool", Content: "sunny", ToolCallID: "tc1"},
		},
	}
	msgs := messagesToOllama(req)
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

func TestToolsToOllama(t *testing.T) {
	specs := []interfaces.ToolSpec{
		{Name: "search", Description: "search the web", Parameters: interfaces.JSONSchema{"type": "object"}},
		{Name: "noparams"}, // nil Parameters -> defaulted object schema
	}
	tools := toolsToOllama(specs)
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

// Ollama's OpenAI layer supports only json_object: a JSON request maps to json_object with or
// without a Schema (the schema is dropped, since json_schema is unsupported).
func TestResponseFormatToOllama(t *testing.T) {
	// Text.
	if rf := responseFormatToOllama(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatText}); rf.OfText == nil {
		t.Fatal("text -> OfText expected")
	}
	// JSON without schema -> json_object.
	if rf := responseFormatToOllama(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON}); rf.OfJSONObject == nil {
		t.Fatal("json (no schema) -> OfJSONObject expected")
	}
	// JSON *with* schema -> still json_object (schema ignored), never json_schema.
	withSchema := &interfaces.ResponseFormat{
		Type:   interfaces.ResponseFormatJSON,
		Name:   "FactAnswer",
		Schema: interfaces.JSONSchema{"type": "object"},
	}
	rf := responseFormatToOllama(withSchema)
	if rf.OfJSONObject == nil {
		t.Fatal("json (with schema) -> OfJSONObject expected")
	}
	if rf.OfJSONSchema != nil {
		t.Fatal("Ollama must not emit json_schema (unsupported)")
	}
}

func TestBuildCompletionParams(t *testing.T) {
	c := newTestClient(t, llm.WithModel("llama3.2"))
	temp, topP := 0.3, 0.9
	req := &interfaces.LLMRequest{
		Messages:       []interfaces.Message{{Role: "user", Content: "hi"}},
		Temperature:    &temp,
		TopP:           &topP,
		MaxTokens:      50,
		Tools:          []interfaces.ToolSpec{{Name: "fn"}},
		ResponseFormat: &interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON},
	}
	params := c.buildCompletionParams(messagesToOllama(req), req)
	if params.Model != "llama3.2" {
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
