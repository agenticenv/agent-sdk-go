// Package ollama provides an interfaces.LLMClient for Ollama, covering both a local daemon
// and Ollama Cloud (ollama.com).
//
// Ollama exposes an OpenAI-compatible Chat Completions API, so this client uses the
// openai-go SDK as its transport but implements the interface independently: it sends
// system prompts with the "system" role, and maps a "reasoning_content" field—emitted by
// thinking models such as deepseek-r1 and qwen3—into LLMResponse.Metadata["reasoning_content"]
// and streaming ThinkingDelta.
//
// # Local
//
// The local daemon requires no API key (Ollama ignores auth, so a placeholder key is
// injected for the transport) and defaults to DefaultBaseURL (http://localhost:11434/v1).
// Common models are any locally pulled model, e.g. "llama3.2", "qwen2.5", "mistral".
//
// # Cloud
//
// Ollama Cloud (https://ollama.com) hosts larger models behind an API key from
// https://ollama.com/settings/keys. Supplying an API key—via llm.WithAPIKey or the
// OLLAMA_API_KEY environment variable—switches the default base URL to DefaultCloudBaseURL
// (https://ollama.com/v1); pass llm.WithBaseURL explicitly to override either default (e.g. a
// self-hosted Ollama behind an auth proxy). Cloud models are suffixed "-cloud", e.g.
// "qwen3-coder:480b-cloud".
//
// Model caveat: not every Ollama model supports tool-calling or JSON mode, so tool use and
// response_format behavior varies by model.
package ollama

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"
	"github.com/openai/openai-go/v3/shared/constant"
)

// DefaultBaseURL is Ollama's local OpenAI-compatible API endpoint.
const DefaultBaseURL = "http://localhost:11434/v1"

// DefaultCloudBaseURL is Ollama Cloud's OpenAI-compatible API endpoint. It is used automatically
// when an API key is set (via llm.WithAPIKey or OLLAMA_API_KEY) and llm.WithBaseURL is not.
const DefaultCloudBaseURL = "https://ollama.com/v1"

// cloudAPIKeyEnvVar is read as a fallback when llm.WithAPIKey is not supplied, matching the
// Ollama CLI's own convention for Ollama Cloud authentication.
const cloudAPIKeyEnvVar = "OLLAMA_API_KEY"

// placeholderAPIKey is injected as the local transport's auth token. The local daemon ignores
// auth, but the openai-go SDK requires a non-empty key.
const placeholderAPIKey = "ollama"

// reasoningContentField is the non-standard field thinking models (deepseek-r1, qwen3) return
// their chain of thought in; the openai-go SDK captures it in JSON.ExtraFields.
const reasoningContentField = "reasoning_content"

var _ interfaces.LLMClient = (*Client)(nil)

// Client implements interfaces.LLMClient for Ollama.
type Client struct {
	llm.LLMConfig
	client openai.Client
}

// NewClient creates a new Ollama LLM client, targeting a local daemon by default.
//
// No API key is required for local use. Supplying one—via llm.WithAPIKey or the
// OLLAMA_API_KEY environment variable (checked when llm.WithAPIKey is not set)—switches the
// default base URL to Ollama Cloud (DefaultCloudBaseURL). llm.WithBaseURL always overrides
// the default, local or cloud, e.g. for a remote self-hosted daemon or an auth proxy.
func NewClient(opts ...llm.Option) (*Client, error) {
	config, err := llm.BuildConfigKeyless(opts...)
	if err != nil {
		return nil, err
	}
	if config.APIKey == "" {
		config.APIKey = os.Getenv(cloudAPIKeyEnvVar)
	}
	if config.BaseURL == "" {
		if config.APIKey != "" {
			config.BaseURL = DefaultCloudBaseURL
		} else {
			config.BaseURL = DefaultBaseURL
		}
	}
	transportKey := config.APIKey
	if transportKey == "" {
		transportKey = placeholderAPIKey
	}
	c := &Client{LLMConfig: *config}
	c.client = openai.NewClient(
		option.WithAPIKey(transportKey),
		option.WithBaseURL(c.BaseURL),
	)
	return c, nil
}

func (c *Client) GetProvider() interfaces.LLMProvider {
	return interfaces.LLMProviderOllama
}

func (c *Client) GetModel() string {
	return c.Model
}

func (c *Client) IsStreamSupported() bool {
	return true
}

// buildCompletionParams builds ChatCompletionNewParams. Sampling (Temperature, MaxTokens,
// TopP) is taken from req when set. Thinking models reason automatically, so req.Reasoning is
// not mapped to a request field.
func (c *Client) buildCompletionParams(messages []openai.ChatCompletionMessageParamUnion, req *interfaces.LLMRequest) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    c.Model,
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = param.NewOpt(int64(req.MaxTokens))
	}
	if req.TopP != nil {
		params.TopP = param.NewOpt(*req.TopP)
	}
	if len(req.Tools) > 0 {
		params.Tools = toolsToOllama(req.Tools)
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		}
	}
	if req.ResponseFormat != nil {
		params.ResponseFormat = responseFormatToOllama(req.ResponseFormat)
	}
	return params
}

// responseFormatToOllama maps the generic ResponseFormat to Ollama's supported set.
// Ollama's OpenAI layer supports plain JSON mode (json_object), not OpenAI's schema-constrained
// json_schema, so a ResponseFormatJSON always maps to json_object and any Schema is ignored
// (not enforced by the API). To get JSON output, the prompt should still ask for it.
func responseFormatToOllama(rf *interfaces.ResponseFormat) openai.ChatCompletionNewParamsResponseFormatUnion {
	switch rf.Type {
	case interfaces.ResponseFormatText:
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfText: &shared.ResponseFormatTextParam{Type: constant.Text("text")},
		}
	case interfaces.ResponseFormatJSON:
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{Type: constant.JSONObject("json_object")},
		}
	default:
		return openai.ChatCompletionNewParamsResponseFormatUnion{}
	}
}

func (c *Client) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	messages := messagesToOllama(req)
	params := c.buildCompletionParams(messages, req)

	// Log safe debug info only (no messages/content to avoid leaking sensitive data).
	c.Logger.Debug(ctx, "generating ollama response",
		slog.String("model", c.Model),
		slog.Int("messageCount", len(req.Messages)),
		slog.Int("toolCount", len(req.Tools)),
		slog.Bool("hasSystemMessage", req.SystemMessage != ""))
	resp, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}
	var contentLen int
	var toolNames []string
	if len(resp.Choices) > 0 {
		m := resp.Choices[0].Message
		contentLen = len(m.Content)
		toolNames = make([]string, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "" {
				toolNames = append(toolNames, tc.Function.Name)
			}
		}
	}
	c.Logger.Debug(ctx, "ollama response generated",
		slog.String("model", resp.Model),
		slog.Int("contentLen", contentLen),
		slog.Int("toolCallCount", len(toolNames)),
		slog.Any("toolNames", toolNames))
	return ollamaResponseToLLM(resp), nil
}

func (c *Client) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	c.Logger.Debug(ctx, "starting ollama stream",
		slog.String("model", c.Model),
		slog.Int("messageCount", len(req.Messages)),
		slog.Int("toolCount", len(req.Tools)))
	messages := messagesToOllama(req)
	params := c.buildCompletionParams(messages, req)
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
	acc := &openai.ChatCompletionAccumulator{}
	return &ollamaStreamAdapter{stream: stream, acc: acc}, nil
}

// ollamaStreamAdapter adapts Ollama's OpenAI-compatible streaming API to interfaces.LLMStream.
type ollamaStreamAdapter struct {
	stream *ssestream.Stream[openai.ChatCompletionChunk]
	acc    *openai.ChatCompletionAccumulator
}

func (a *ollamaStreamAdapter) Next() bool { return a.stream.Next() }
func (a *ollamaStreamAdapter) Err() error { return a.stream.Err() }
func (a *ollamaStreamAdapter) Current() *interfaces.LLMStreamChunk {
	chunk := a.stream.Current()
	a.acc.AddChunk(chunk)
	out := &interfaces.LLMStreamChunk{}
	if len(chunk.Choices) > 0 {
		delta := chunk.Choices[0].Delta
		out.ContentDelta = delta.Content
		// Thinking models stream their chain of thought in reasoning_content.
		out.ThinkingDelta = extractReasoning(delta.JSON.ExtraFields)
	}
	return out
}
func (a *ollamaStreamAdapter) GetResult() *interfaces.LLMResponse {
	if len(a.acc.Choices) == 0 {
		return nil
	}
	return ollamaResponseToLLM(&a.acc.ChatCompletion)
}

func ollamaResponseToLLM(resp *openai.ChatCompletion) *interfaces.LLMResponse {
	out := &interfaces.LLMResponse{
		Content: resp.Choices[0].Message.Content,
		Metadata: map[string]any{
			"model": resp.Model,
		},
		Usage: ollamaUsageToLLM(resp.Usage),
	}
	msg := resp.Choices[0].Message
	if reasoning := extractReasoning(msg.JSON.ExtraFields); reasoning != "" {
		out.Metadata[reasoningContentField] = reasoning
	}
	for _, tc := range msg.ToolCalls {
		if tc.Function.Name == "" {
			continue
		}
		args := make(map[string]any)
		if tc.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		out.ToolCalls = append(out.ToolCalls, &interfaces.ToolCall{
			ToolCallID: tc.ID,
			ToolName:   tc.Function.Name,
			Args:       args,
		})
	}
	return out
}

// extractReasoning returns the decoded reasoning_content extra field, or "".
// Thinking models return reasoning under this non-standard field; the openai-go SDK captures
// it in JSON.ExtraFields. Note Valid() is false for extra fields, so guard on Raw().
func extractReasoning(extra map[string]respjson.Field) string {
	f, ok := extra[reasoningContentField]
	if !ok {
		return ""
	}
	raw := f.Raw() // JSON-encoded
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

// ollamaUsageToLLM maps OpenAI-shaped CompletionUsage to interfaces.LLMUsage.
func ollamaUsageToLLM(u openai.CompletionUsage) *interfaces.LLMUsage {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	out := &interfaces.LLMUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.PromptTokensDetails.CachedTokens != 0 {
		out.CachedPromptTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails.ReasoningTokens != 0 {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

func messagesToOllama(req *interfaces.LLMRequest) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	if req.SystemMessage != "" {
		// Ollama's OpenAI layer expects the "system" role (not OpenAI's "developer" role).
		out = append(out, openai.SystemMessage(req.SystemMessage))
	}
	if len(req.Messages) == 0 {
		return out
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			out = append(out, openai.UserMessage(m.Content))
		case "assistant":
			if len(m.ToolCalls) > 0 {
				toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					argsBytes, _ := json.Marshal(tc.Args)
					argsStr := "{}"
					if len(argsBytes) > 0 {
						argsStr = string(argsBytes)
					}
					name := strings.TrimSpace(tc.ToolName)
					if name == "" {
						name = "tool"
					}
					toolCalls[i] = openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: tc.ToolCallID,
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Arguments: argsStr,
								Name:      name,
							},
						},
					}
				}
				out = append(out, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(m.Content)},
						ToolCalls: toolCalls,
					},
				})
			} else {
				out = append(out, openai.AssistantMessage(m.Content))
			}
		case "tool":
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return out
}

func toolsToOllama(specs []interfaces.ToolSpec) []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, len(specs))
	for i, s := range specs {
		params := shared.FunctionParameters(map[string]any(s.Parameters))
		if params == nil {
			params = shared.FunctionParameters{"type": "object", "properties": map[string]any{}}
		}
		out[i] = openai.ChatCompletionToolUnionParam{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        s.Name,
					Description: openai.String(s.Description),
					Parameters:  params,
				},
			},
		}
	}
	return out
}
