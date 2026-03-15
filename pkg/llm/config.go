package llm

import (
	"errors"

	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/logger"
	"go.temporal.io/sdk/log"
)

type LLMConfig struct {
	provider interfaces.LLMProvider
	APIKey   string
	Model    string
	BaseURL  string
	Logger   log.Logger
	LogLevel string
}

type Option func(*LLMConfig)

func WithLogger(logger log.Logger) Option {
	return func(c *LLMConfig) { c.Logger = logger }
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

func BuildConfig(opts ...Option) (*LLMConfig, error) {
	c := &LLMConfig{}
	for _, opt := range opts {
		opt(c)
	}
	if c.LogLevel == "" {
		c.LogLevel = "error"
	}
	if c.Logger == nil {
		c.Logger = logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{Level: c.LogLevel}))
	}
	if c.APIKey == "" {
		return nil, errors.New("APIKey is required")
	}
	return c, nil
}
