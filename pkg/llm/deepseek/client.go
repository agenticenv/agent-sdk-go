// Package deepseek provides an interfaces.LLMClient for DeepSeek.
//
// DeepSeek exposes an OpenAI-compatible Chat Completions API, so this client uses the
// openai-go SDK as its transport but implements the interface independently: it defaults
// to DeepSeek's base URL, sends system prompts with the "system" role (DeepSeek does not
// accept OpenAI's "developer" role), and maps the deepseek-reasoner "reasoning_content"
// field into LLMResponse.Metadata["reasoning_content"] and streaming ThinkingDelta.
//
// Common models: "deepseek-chat" and "deepseek-reasoner".
package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// DefaultBaseURL is DeepSeek's OpenAI-compatible API endpoint.
const DefaultBaseURL = "https://api.deepseek.com"

// reasoningContentField is the non-standard field deepseek-reasoner returns its chain of
// thought in; the openai-go SDK captures it in JSON.ExtraFields.
const reasoningContentField = "reasoning_content"

var _ interfaces.LLMClient = (*Client)(nil)

// Client implements interfaces.LLMClient for DeepSeek.
type Client struct {
	llm.LLMConfig
	client openai.Client
}

// NewClient creates a new DeepSeek LLM client. The base URL defaults to DefaultBaseURL;
// a caller-supplied llm.WithBaseURL overrides it.
func NewClient(opts ...llm.Option) (*Client, error) {
	config, err := llm.BuildConfig(opts...)
	if err != nil {
		return nil, err
	}
	if config.BaseURL == "" {
		config.BaseURL = DefaultBaseURL
	}
	c := &Client{LLMConfig: *config}
	c.client = openai.NewClient(
		option.WithAPIKey(c.APIKey),
		option.WithBaseURL(c.BaseURL),
	)
	return c, nil
}

func (c *Client) GetProvider() interfaces.LLMProvider {
	return interfaces.LLMProviderDeepSeek
}

func (c *Client) GetModel() string {
	return c.Model
}

func (c *Client) IsStreamSupported() bool {
	return true
}

// buildCompletionParams builds ChatCompletionNewParams. Sampling (Temperature, MaxTokens,
// TopP) is taken from req when set. DeepSeek does not use OpenAI's reasoning_effort param;
// deepseek-reasoner reasons automatically, so req.Reasoning is not mapped to a request field.
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
		params.Tools = toolsToDeepSeek(req.Tools)
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		}
	}
	if req.ResponseFormat != nil {
		params.ResponseFormat = responseFormatToDeepSeek(req.ResponseFormat)
	}
	return params
}

// responseFormatToDeepSeek maps the generic ResponseFormat to DeepSeek's supported set.
// DeepSeek supports only json_object (plain JSON mode), not OpenAI's schema-constrained
// json_schema, so a ResponseFormatJSON always maps to json_object and any Schema is ignored
// (the schema is not enforced by the API). To get JSON output, the prompt should still ask
// for it (DeepSeek requires the word "json" to appear in the conversation).
func responseFormatToDeepSeek(rf *interfaces.ResponseFormat) openai.ChatCompletionNewParamsResponseFormatUnion {
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
	messages := messagesToDeepSeek(req)
	params := c.buildCompletionParams(messages, req)

	// Log safe debug info only (no messages/content to avoid leaking sensitive data).
	c.Logger.Debug(ctx, "generating deepseek response",
		slog.String("model", c.Model),
		slog.Int("messageCount", len(req.Messages)),
		slog.Int("toolCount", len(req.Tools)),
		slog.Bool("hasSystemMessage", req.SystemMessage != ""))
	resp, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, wrapLLM(err)
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
	c.Logger.Debug(ctx, "deepseek response generated",
		slog.String("model", resp.Model),
		slog.Int("contentLen", contentLen),
		slog.Int("toolCallCount", len(toolNames)),
		slog.Any("toolNames", toolNames))
	return deepSeekResponseToLLM(resp), nil
}

func (c *Client) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	c.Logger.Debug(ctx, "starting deepseek stream",
		slog.String("model", c.Model),
		slog.Int("messageCount", len(req.Messages)),
		slog.Int("toolCount", len(req.Tools)))
	messages := messagesToDeepSeek(req)
	params := c.buildCompletionParams(messages, req)
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
	acc := &openai.ChatCompletionAccumulator{}
	return &deepSeekStreamAdapter{stream: stream, acc: acc}, nil
}

// deepSeekStreamAdapter adapts DeepSeek's OpenAI-compatible streaming API to interfaces.LLMStream.
type deepSeekStreamAdapter struct {
	stream *ssestream.Stream[openai.ChatCompletionChunk]
	acc    *openai.ChatCompletionAccumulator
}

func (a *deepSeekStreamAdapter) Next() bool { return a.stream.Next() }
func (a *deepSeekStreamAdapter) Err() error { return wrapLLM(a.stream.Err()) }

func wrapLLM(err error) error {
	if err == nil {
		return nil
	}
	var api *openai.Error
	if errors.As(err, &api) {
		return llm.Classify(err, api.StatusCode, llm.RetryAfterFromResponse(api.Response), api.Type, api.Code, api.Message)
	}
	return llm.Classify(err, 0, 0)
}
func (a *deepSeekStreamAdapter) Current() *interfaces.LLMStreamChunk {
	chunk := a.stream.Current()
	a.acc.AddChunk(chunk)
	out := &interfaces.LLMStreamChunk{}
	if len(chunk.Choices) > 0 {
		delta := chunk.Choices[0].Delta
		out.ContentDelta = delta.Content
		// deepseek-reasoner streams its chain of thought in reasoning_content.
		out.ThinkingDelta = extractReasoning(delta.JSON.ExtraFields)
	}
	return out
}
func (a *deepSeekStreamAdapter) GetResult() *interfaces.LLMResponse {
	if len(a.acc.Choices) == 0 {
		return nil
	}
	return deepSeekResponseToLLM(&a.acc.ChatCompletion)
}

func deepSeekResponseToLLM(resp *openai.ChatCompletion) *interfaces.LLMResponse {
	out := &interfaces.LLMResponse{
		Content: resp.Choices[0].Message.Content,
		Metadata: map[string]any{
			"model": resp.Model,
		},
		Usage: deepSeekUsageToLLM(resp.Usage),
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
// deepseek-reasoner returns reasoning under this non-standard field; the openai-go SDK
// captures it in JSON.ExtraFields. Note Valid() is false for extra fields, so guard on Raw().
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

// deepSeekUsageToLLM maps OpenAI-shaped CompletionUsage to interfaces.LLMUsage.
func deepSeekUsageToLLM(u openai.CompletionUsage) *interfaces.LLMUsage {
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

func messagesToDeepSeek(req *interfaces.LLMRequest) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	if req.SystemMessage != "" {
		// DeepSeek expects the "system" role (not OpenAI's "developer" role).
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

func toolsToDeepSeek(specs []interfaces.ToolSpec) []openai.ChatCompletionToolUnionParam {
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
