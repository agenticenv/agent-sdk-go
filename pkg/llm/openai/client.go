package openai

import (
	"context"
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"
	"github.com/openai/openai-go/v3/shared/constant"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/llm"
	"go.uber.org/zap"
)

var _ interfaces.LLMClient = (*Client)(nil)

// Client implements interfaces.LLMClient for OpenAI.
type Client struct {
	llm.LLMConfig
	client openai.Client
}

// NewClient creates a new OpenAI LLM client.
func NewClient(opts ...llm.Option) (*Client, error) {
	config, err := llm.BuildConfig(opts...)
	if err != nil {
		return nil, err
	}
	c := &Client{
		LLMConfig: *config,
	}
	options := []option.RequestOption{option.WithAPIKey(c.APIKey)}
	if c.BaseURL != "" {
		options = append(options, option.WithBaseURL(c.BaseURL))
	}
	c.client = openai.NewClient(options...)
	return c, nil
}

func (c *Client) GetProvider() interfaces.LLMProvider {
	return interfaces.LLMProviderOpenAI
}

func (c *Client) GetModel() string {
	return c.Model
}

func (c *Client) IsStreamSupported() bool {
	return true
}

// buildCompletionParams builds ChatCompletionNewParams. Sampling (Temperature, MaxTokens, TopP) from req when set.
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
		params.Tools = toolsToOpenAI(req.Tools)
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		}
	}
	if req.ResponseFormat != nil {
		params.ResponseFormat = responseFormatToOpenAI(req.ResponseFormat)
	}
	return params
}

func responseFormatToOpenAI(rf *interfaces.ResponseFormat) openai.ChatCompletionNewParamsResponseFormatUnion {
	switch rf.Type {
	case interfaces.ResponseFormatText:
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfText: &shared.ResponseFormatTextParam{Type: constant.Text("text")},
		}
	case interfaces.ResponseFormatJSON:
		if len(rf.Schema) > 0 {
			name := rf.Name
			if name == "" {
				name = "response"
			}
			return openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
					Type: constant.JSONSchema("json_schema"),
					JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
						Name:   name,
						Schema: map[string]any(rf.Schema),
					},
				},
			}
		}
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{Type: constant.JSONObject("json_object")},
		}
	default:
		return openai.ChatCompletionNewParamsResponseFormatUnion{}
	}
}

func (c *Client) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	messages := messagesToOpenAI(req)
	params := c.buildCompletionParams(messages, req)

	// Log safe debug info only (no messages/content to avoid leaking sensitive data)
	c.Logger.Debug("generating openai response",
		zap.String("model", c.Model),
		zap.Int("messageCount", len(req.Messages)),
		zap.Int("toolCount", len(req.Tools)),
		zap.Bool("hasSystemMessage", req.SystemMessage != ""))
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
	c.Logger.Debug("openai response generated",
		zap.String("model", resp.Model),
		zap.Int("contentLen", contentLen),
		zap.Int("toolCallCount", len(toolNames)),
		zap.Strings("toolNames", toolNames))
	return openAIResponseToLLM(resp), nil
}

func (c *Client) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	c.Logger.Debug("starting openai stream",
		zap.String("model", c.Model),
		zap.Int("messageCount", len(req.Messages)),
		zap.Int("toolCount", len(req.Tools)))
	messages := messagesToOpenAI(req)
	params := c.buildCompletionParams(messages, req)
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
