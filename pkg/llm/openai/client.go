package openai

import (
	"context"
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm"
)

var _ interfaces.LLMClient = (*Client)(nil)

// Client implements interfaces.LLMClient for OpenAI.
type Client struct {
	config *llm.LLMConfig
	client openai.Client
}

// NewClient creates a new OpenAI LLM client.
func NewClient(config *llm.LLMConfig) interfaces.LLMClient {
	opts := []option.RequestOption{option.WithAPIKey(config.APIKey)}
	if config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(config.BaseURL))
	}
	return &Client{
		config: config,
		client: openai.NewClient(opts...),
	}
}

func (c *Client) Model() string {
	return c.config.Model
}

func (c *Client) IsStreamSupported() bool {
	return true
}

func (c *Client) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	messages := messagesToOpenAI(req)
	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    c.config.Model,
	}
	if len(req.Tools) > 0 {
		params.Tools = toolsToOpenAI(req.Tools)
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		}
	}
	resp, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return openAIResponseToLLM(resp), nil
}

func (c *Client) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	messages := messagesToOpenAI(req)
	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    c.config.Model,
	}
	if len(req.Tools) > 0 {
		params.Tools = toolsToOpenAI(req.Tools)
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		}
	}
	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
	acc := &openai.ChatCompletionAccumulator{}
	return &openAIStreamAdapter{stream: stream, acc: acc}, nil
}

// openAIStreamAdapter adapts OpenAI's streaming API to interfaces.LLMStream.
type openAIStreamAdapter struct {
	stream *ssestream.Stream[openai.ChatCompletionChunk]
	acc    *openai.ChatCompletionAccumulator
}

func (a *openAIStreamAdapter) Next() bool { return a.stream.Next() }
func (a *openAIStreamAdapter) Err() error { return a.stream.Err() }
func (a *openAIStreamAdapter) Current() *interfaces.LLMStreamChunk {
	chunk := a.stream.Current()
	a.acc.AddChunk(chunk)
	out := &interfaces.LLMStreamChunk{}
	if len(chunk.Choices) > 0 {
		out.ContentDelta = chunk.Choices[0].Delta.Content
	}
	return out
}
func (a *openAIStreamAdapter) GetResult() *interfaces.LLMResponse {
	if len(a.acc.Choices) == 0 {
		return nil
	}
	return openAIResponseToLLM(&a.acc.ChatCompletion)
}

func openAIResponseToLLM(resp *openai.ChatCompletion) *interfaces.LLMResponse {
	out := &interfaces.LLMResponse{
		Content: resp.Choices[0].Message.Content,
		Metadata: map[string]any{
			"model": resp.Model,
		},
	}
	msg := resp.Choices[0].Message
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

func messagesToOpenAI(req *interfaces.LLMRequest) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	if req.SystemMessage != "" {
		out = append(out, openai.DeveloperMessage(req.SystemMessage))
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
					toolCalls[i] = openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: tc.ToolCallID,
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Arguments: argsStr,
								Name:      tc.ToolName,
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

func toolsToOpenAI(specs []interfaces.ToolSpec) []openai.ChatCompletionToolUnionParam {
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
