package anthropic

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
)

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
