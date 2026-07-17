package llm

import (
	"errors"

	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

type LLMConfig struct {
	APIKey   string
	Model    string
	BaseURL  string
	Logger   logger.Logger
	LogLevel string
	// PromptCaching is Anthropic-only; nil means disabled. Set via WithPromptCaching.
	PromptCaching *bool
}

type Option func(*LLMConfig)

func WithLogger(l logger.Logger) Option {
	return func(c *LLMConfig) { c.Logger = l }
}

func WithLogLevel(level string) Option {
	return func(c *LLMConfig) { c.LogLevel = level }
}

func WithAPIKey(apiKey string) Option {
	return func(c *LLMConfig) { c.APIKey = apiKey }
}

func WithModel(model string) Option {
	return func(c *LLMConfig) { c.Model = model }
}

func WithBaseURL(baseURL string) Option {
	return func(c *LLMConfig) { c.BaseURL = baseURL }
}

// WithPromptCaching enables or disables Anthropic prompt-cache breakpoints.
// Default when unset is disabled (write cost / short TTL are a poor fit at low volume).
// OpenAI/Gemini/DeepSeek ignore this option.
func WithPromptCaching(enabled bool) Option {
	return func(c *LLMConfig) { c.PromptCaching = &enabled }
}

// DefaultMaxTokens is used when MaxTokens is 0 and the provider requires it (e.g. Anthropic).
const DefaultMaxTokens = 1024

// BuildConfigKeyless builds LLMConfig from options without requiring an APIKey.
// It is for keyless/local providers (e.g. Ollama) whose transport ignores auth.
// Defaults when not set:
//   - LogLevel: "error"
//   - Logger: stderr slog logger at LogLevel
//
// Sampling (Temperature, MaxTokens, TopP, TopK) is per-agent—use agent.WithTemperature etc.
func BuildConfigKeyless(opts ...Option) (*LLMConfig, error) {
	c := &LLMConfig{}
	for _, opt := range opts {
		opt(c)
	}
	if c.LogLevel == "" {
		c.LogLevel = "error"
	}
	if c.Logger == nil {
		c.Logger = logger.DefaultLogger(c.LogLevel)
	}
	return c, nil
}

// BuildConfig builds LLMConfig from options, requiring a non-empty APIKey.
// For keyless/local providers use BuildConfigKeyless. Defaults when not set:
//   - LogLevel: "error"
//   - Logger: stderr slog logger at LogLevel
//
// Sampling (Temperature, MaxTokens, TopP, TopK) is per-agent—use agent.WithTemperature etc.
func BuildConfig(opts ...Option) (*LLMConfig, error) {
	c, err := BuildConfigKeyless(opts...)
	if err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("APIKey is required")
	}
	return c, nil
}
