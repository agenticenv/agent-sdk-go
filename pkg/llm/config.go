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

// DefaultMaxTokens is used when MaxTokens is 0 and the provider requires it (e.g. Anthropic).
const DefaultMaxTokens = 1024

// BuildConfig builds LLMConfig from options. Defaults when not set:
//   - LogLevel: "error"
//   - Logger: stderr slog logger at LogLevel
//
// Sampling (Temperature, MaxTokens, TopP, TopK) is per-agent—use agent.WithTemperature etc.
func BuildConfig(opts ...Option) (*LLMConfig, error) {
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
	if c.APIKey == "" {
		return nil, errors.New("APIKey is required")
	}
	return c, nil
}
