package anthropic

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/vvsynapse/temporal-agents-go/pkg/interfaces"
	"github.com/vvsynapse/temporal-agents-go/pkg/llm"
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
