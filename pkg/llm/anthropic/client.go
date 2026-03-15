package anthropic

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm"
	"go.uber.org/zap"
)

var _ interfaces.LLMClient = (*Client)(nil)

// Client implements interfaces.LLMClient for Anthropic Claude.
type Client struct {
	llm.LLMConfig
	client anthropic.Client
}

// NewClient creates a new Anthropic LLM client.
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
	c.client = anthropic.NewClient(options...)
	return c, nil
}

func (c *Client) GetProvider() interfaces.LLMProvider {
	return interfaces.LLMProviderAnthropic
}

func (c *Client) GetModel() string {
	return c.Model
}

func (c *Client) IsStreamSupported() bool {
	return true
}

func (c *Client) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	messages := messagesToAnthropic(req)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.Model),
		MaxTokens: 1024,
		Messages:  messages,
	}
	if req.SystemMessage != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: req.SystemMessage},
		}
	}
	if len(req.Tools) > 0 {
		params.Tools = toolsToAnthropic(req.Tools)
		params.ToolChoice = anthropic.ToolChoiceUnionParam{
			OfAuto: &anthropic.ToolChoiceAutoParam{},
		}
	}
	// Log safe debug info only (no messages/content to avoid leaking sensitive data)
	c.Logger.Debug("generating anthropic response",
		zap.String("model", c.Model),
		zap.Int("messageCount", len(req.Messages)),
		zap.Int("toolCount", len(req.Tools)),
		zap.Bool("hasSystemMessage", req.SystemMessage != ""))
	msg, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, err
	}
	content, toolCalls := extractContentAndToolCalls(msg.Content)
	toolNames := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc != nil && tc.ToolName != "" {
			toolNames = append(toolNames, tc.ToolName)
		}
	}
	c.Logger.Debug("anthropic response generated",
		zap.String("model", string(msg.Model)),
		zap.String("stopReason", string(msg.StopReason)),
		zap.Int("contentLen", len(content)),
		zap.Int("toolCallCount", len(toolNames)),
		zap.Strings("toolNames", toolNames))
	return &interfaces.LLMResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Metadata: map[string]any{
			"model":       string(msg.Model),
			"stop_reason": string(msg.StopReason),
		},
	}, nil
}

func (c *Client) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	c.Logger.Debug("starting anthropic stream",
		zap.String("model", c.Model),
		zap.Int("messageCount", len(req.Messages)),
		zap.Int("toolCount", len(req.Tools)))
	messages := messagesToAnthropic(req)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.Model),
		MaxTokens: 1024,
		Messages:  messages,
	}
	if req.SystemMessage != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: req.SystemMessage},
		}
	}
	if len(req.Tools) > 0 {
		params.Tools = toolsToAnthropic(req.Tools)
		params.ToolChoice = anthropic.ToolChoiceUnionParam{
			OfAuto: &anthropic.ToolChoiceAutoParam{},
		}
	}
	stream := c.client.Messages.NewStreaming(ctx, params)
	acc := &anthropic.Message{}
	return &anthropicStreamAdapter{stream: stream, acc: acc}, nil
}

// anthropicStreamAdapter adapts Anthropic's streaming API to interfaces.LLMStream.
type anthropicStreamAdapter struct {
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion]
	acc    *anthropic.Message
}

func (a *anthropicStreamAdapter) Next() bool { return a.stream.Next() }
func (a *anthropicStreamAdapter) Err() error { return a.stream.Err() }
func (a *anthropicStreamAdapter) Current() *interfaces.LLMStreamChunk {
	event := a.stream.Current()
	out := &interfaces.LLMStreamChunk{}

	if err := a.acc.Accumulate(event); err != nil {
		return out
	}

	switch ev := event.AsAny().(type) {
	case anthropic.ContentBlockDeltaEvent:
		switch delta := ev.Delta.AsAny().(type) {
		case anthropic.TextDelta:
			out.ContentDelta = delta.Text
		case anthropic.ThinkingDelta:
			out.ThinkingDelta = delta.Thinking
		}
	}

	return out
}
func (a *anthropicStreamAdapter) GetResult() *interfaces.LLMResponse {
	content, toolCalls := extractContentAndToolCalls(a.acc.Content)
	return &interfaces.LLMResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Metadata: map[string]any{
			"model":       string(a.acc.Model),
			"stop_reason": string(a.acc.StopReason),
		},
	}
}

func messagesToAnthropic(req *interfaces.LLMRequest) []anthropic.MessageParam {
	if len(req.Messages) == 0 {
		return []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("")),
		}
	}
	var out []anthropic.MessageParam
	i := 0
	for i < len(req.Messages) {
		m := req.Messages[i]
		switch m.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			var blocks []anthropic.ContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				if tc == nil {
					continue
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ToolCallID, tc.Args, tc.ToolName))
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			}
		case "tool":
			var toolBlocks []anthropic.ContentBlockParamUnion
			for i < len(req.Messages) && req.Messages[i].Role == "tool" {
				t := req.Messages[i]
				toolBlocks = append(toolBlocks, anthropic.NewToolResultBlock(t.ToolCallID, t.Content, false))
				i++
			}
			if len(toolBlocks) > 0 {
				out = append(out, anthropic.NewUserMessage(toolBlocks...))
			}
			continue
		}
		i++
	}
	return out
}

func extractContentAndToolCalls(blocks []anthropic.ContentBlockUnion) (string, []*interfaces.ToolCall) {
	var content string
	var toolCalls []*interfaces.ToolCall
	for _, block := range blocks {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			args := make(map[string]any)
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &args)
			}
			toolCalls = append(toolCalls, &interfaces.ToolCall{
				ToolCallID: block.ID,
				ToolName:   block.Name,
				Args:       args,
			})
		}
	}
	return content, toolCalls
}

func toolsToAnthropic(specs []interfaces.ToolSpec) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(specs))
	for i, s := range specs {
		schema := toolInputSchema(s.Parameters)
		out[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        s.Name,
				Description: anthropic.String(s.Description),
				InputSchema: schema,
			},
		}
	}
	return out
}

func toolInputSchema(params interfaces.JSONSchema) anthropic.ToolInputSchemaParam {
	schema := anthropic.ToolInputSchemaParam{
		Type: constant.Object("object"),
	}
	if params == nil {
		schema.Properties = map[string]any{}
		return schema
	}
	if p, ok := params["properties"].(map[string]any); ok {
		schema.Properties = p
	} else {
		schema.Properties = map[string]any{}
	}
	if r, ok := params["required"].([]any); ok {
		for _, v := range r {
			if s, ok := v.(string); ok {
				schema.Required = append(schema.Required, s)
			}
		}
	} else if r, ok := params["required"].([]string); ok {
		schema.Required = r
	}
	return schema
}
