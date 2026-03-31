package gemini

import (
	"context"
	"encoding/json"
	"iter"
	"sync"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"go.uber.org/zap"
	"google.golang.org/genai"
)

var _ interfaces.LLMClient = (*Client)(nil)

// Client implements interfaces.LLMClient for Google Gemini.
type Client struct {
	llm.LLMConfig
	client *genai.Client
}

// NewClient creates a new Gemini LLM client.
func NewClient(opts ...llm.Option) (*Client, error) {
	config, err := llm.BuildConfig(opts...)
	if err != nil {
		return nil, err
	}
	clientConfig := &genai.ClientConfig{
		APIKey:  config.APIKey,
		Backend: genai.BackendGeminiAPI,
	}
	if config.BaseURL != "" {
		clientConfig.HTTPOptions = genai.HTTPOptions{BaseURL: config.BaseURL}
	}
	client, err := genai.NewClient(context.Background(), clientConfig)
	if err != nil {
		return nil, err
	}
	return &Client{
		LLMConfig: *config,
		client:    client,
	}, nil
}

func (c *Client) GetProvider() interfaces.LLMProvider {
	return interfaces.LLMProviderGemini
}

func (c *Client) GetModel() string {
	return c.Model
}

func (c *Client) IsStreamSupported() bool {
	return true
}

func (c *Client) buildConfig(req *interfaces.LLMRequest) *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{}
	if req.SystemMessage != "" {
		cfg.SystemInstruction = genai.NewContentFromText(req.SystemMessage, genai.RoleUser)
	}
	maxTokens := int32(llm.DefaultMaxTokens)
	if req.MaxTokens > 0 {
		maxTokens = int32(req.MaxTokens)
	}
	cfg.MaxOutputTokens = maxTokens
	if req.Temperature != nil {
		t := float32(*req.Temperature)
		cfg.Temperature = &t
	}
	if req.TopP != nil {
		p := float32(*req.TopP)
		cfg.TopP = &p
	}
	if req.TopK != nil {
		k := float32(*req.TopK)
		cfg.TopK = &k
	}
	if len(req.Tools) > 0 {
		cfg.Tools = toolsToGemini(req.Tools)
		cfg.ToolConfig = &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAuto,
			},
		}
	}
	if req.ResponseFormat != nil {
		applyResponseFormat(cfg, req.ResponseFormat)
	}
	return cfg
}

func applyResponseFormat(cfg *genai.GenerateContentConfig, rf *interfaces.ResponseFormat) {
	switch rf.Type {
	case interfaces.ResponseFormatJSON:
		if len(rf.Schema) > 0 {
			cfg.ResponseMIMEType = "application/json"
			cfg.ResponseJsonSchema = map[string]any(rf.Schema)
		}
	case interfaces.ResponseFormatText:
		// default
	}
}

func (c *Client) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	contents := messagesToGemini(req)
	config := c.buildConfig(req)

	c.Logger.Debug("generating gemini response",
		zap.String("model", c.Model),
		zap.Int("messageCount", len(req.Messages)),
		zap.Int("toolCount", len(req.Tools)),
		zap.Bool("hasSystemMessage", req.SystemMessage != ""))

	resp, err := c.client.Models.GenerateContent(ctx, c.Model, contents, config)
	if err != nil {
		return nil, err
	}

	content := resp.Text()
	toolCalls := geminiToolCallsToInterface(resp.FunctionCalls())

	toolNames := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc != nil && tc.ToolName != "" {
			toolNames = append(toolNames, tc.ToolName)
		}
	}
	c.Logger.Debug("gemini response generated",
		zap.Int("contentLen", len(content)),
		zap.Int("toolCallCount", len(toolNames)),
		zap.Strings("toolNames", toolNames))

	metadata := map[string]any{}
	if len(resp.Candidates) > 0 && resp.Candidates[0].FinishReason != "" {
		metadata["finish_reason"] = resp.Candidates[0].FinishReason
	}
	if resp.ModelVersion != "" {
		metadata["model_version"] = resp.ModelVersion
	}

	return &interfaces.LLMResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Metadata:  metadata,
	}, nil
}

func (c *Client) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	c.Logger.Debug("starting gemini stream",
		zap.String("model", c.Model),
		zap.Int("messageCount", len(req.Messages)),
		zap.Int("toolCount", len(req.Tools)))
	contents := messagesToGemini(req)
	config := c.buildConfig(req)
	seq := c.client.Models.GenerateContentStream(ctx, c.Model, contents, config)
	return newGeminiStreamAdapter(seq), nil
}

// geminiStreamAdapter adapts genai's iter.Seq2 to interfaces.LLMStream.
type geminiStreamAdapter struct {
	ch           chan streamChunk
	mu           sync.Mutex
	result       *interfaces.LLMResponse
	err          error
	contentDelta string
}

type streamChunk struct {
	resp *genai.GenerateContentResponse
	err  error
}

func newGeminiStreamAdapter(seq iter.Seq2[*genai.GenerateContentResponse, error]) *geminiStreamAdapter {
	a := &geminiStreamAdapter{ch: make(chan streamChunk, 4)}
	go func() {
		defer close(a.ch)
		seq(func(resp *genai.GenerateContentResponse, err error) bool {
			a.ch <- streamChunk{resp: resp, err: err}
			return err == nil
		})
	}()
	return a
}

func (a *geminiStreamAdapter) Next() bool {
	chunk, ok := <-a.ch
	if !ok {
		return false
	}
	a.mu.Lock()
	a.err = chunk.err
	if chunk.resp != nil {
		content := chunk.resp.Text()
		// Compute delta from previous content (Gemini streams cumulative text)
		prevLen := 0
		if a.result != nil {
			prevLen = len(a.result.Content)
		}
		if prevLen < len(content) {
			a.contentDelta = content[prevLen:]
		} else {
			a.contentDelta = ""
		}
		a.result = &interfaces.LLMResponse{
			Content:   content,
			ToolCalls: geminiToolCallsToInterface(chunk.resp.FunctionCalls()),
			Metadata:  map[string]any{},
		}
		if len(chunk.resp.Candidates) > 0 && chunk.resp.Candidates[0].FinishReason != "" {
			a.result.Metadata["finish_reason"] = chunk.resp.Candidates[0].FinishReason
		}
	}
	a.mu.Unlock()
	return true
}

func (a *geminiStreamAdapter) Current() *interfaces.LLMStreamChunk {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &interfaces.LLMStreamChunk{}
	if a.result != nil {
		out.ContentDelta = a.contentDelta
		out.ToolCalls = a.result.ToolCalls
	}
	return out
}

func (a *geminiStreamAdapter) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

func (a *geminiStreamAdapter) GetResult() *interfaces.LLMResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result
}

func messagesToGemini(req *interfaces.LLMRequest) []*genai.Content {
	if len(req.Messages) == 0 {
		return []*genai.Content{genai.NewContentFromText("", genai.RoleUser)}
	}
	var out []*genai.Content
	i := 0
	for i < len(req.Messages) {
		m := req.Messages[i]
		switch m.Role {
		case "user":
			out = append(out, genai.NewContentFromText(m.Content, genai.RoleUser))
		case "assistant":
			var parts []*genai.Part
			if m.Content != "" {
				parts = append(parts, &genai.Part{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				if tc == nil {
					continue
				}
				parts = append(parts, genai.NewPartFromFunctionCall(tc.ToolName, tc.Args))
			}
			if len(parts) > 0 {
				out = append(out, &genai.Content{Parts: parts, Role: genai.RoleModel})
			}
		case "tool":
			var toolParts []*genai.Part
			for i < len(req.Messages) && req.Messages[i].Role == "tool" {
				t := req.Messages[i]
				resp := parseToolResponse(t.Content)
				fr := &genai.FunctionResponse{Name: t.ToolName, Response: resp}
				if t.ToolCallID != "" {
					fr.ID = t.ToolCallID
				}
				toolParts = append(toolParts, &genai.Part{FunctionResponse: fr})
				i++
			}
			if len(toolParts) > 0 {
				out = append(out, &genai.Content{Parts: toolParts, Role: genai.RoleUser})
			}
			continue
		}
		i++
	}
	return out
}

// parseToolResponse parses tool content as JSON if possible; otherwise wraps as {"result": content}.
func parseToolResponse(content string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(content), &m); err == nil && m != nil {
		return m
	}
	return map[string]any{"result": content}
}

func toolsToGemini(specs []interfaces.ToolSpec) []*genai.Tool {
	if len(specs) == 0 {
		return nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(specs))
	for _, s := range specs {
		params := s.Parameters
		if params == nil {
			params = interfaces.JSONSchema{}
		}
		decls = append(decls, &genai.FunctionDeclaration{
			Name:                 s.Name,
			Description:          s.Description,
			ParametersJsonSchema: map[string]any(params),
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

func geminiToolCallsToInterface(calls []*genai.FunctionCall) []*interfaces.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]*interfaces.ToolCall, 0, len(calls))
	for _, fc := range calls {
		if fc == nil || fc.Name == "" {
			continue
		}
		args := fc.Args
		if args == nil {
			args = make(map[string]any)
		}
		out = append(out, &interfaces.ToolCall{
			ToolCallID: fc.ID,
			ToolName:   fc.Name,
			Args:       args,
		})
	}
	return out
}
