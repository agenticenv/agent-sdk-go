package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/anthropic"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/gemini"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/openai"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/joho/godotenv"
)

type Config struct {
	Host      string
	Port      int
	Namespace string
	TaskQueue string
	LogLevel  string
	Provider  interfaces.LLMProvider
	APIKey    string
	Model     string
	// BaseURL is optional and only used for the OpenAI client (custom or Azure-compatible endpoints).
	// Ignored for Anthropic and Gemini.
	BaseURL string
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func init() {
	// Try .env in cwd, then parent (project root when run from examples/)
	_ = godotenv.Load(".env")
}

// LoadFromEnv loads config from environment variables. .env is loaded on package init if present.
func LoadFromEnv() *Config {
	cfg := &Config{
		Host:      getEnv("TEMPORAL_HOST", "localhost"),
		Port:      getEnvInt("TEMPORAL_PORT", 7233),
		Namespace: getEnv("TEMPORAL_NAMESPACE", "default"),
		TaskQueue: getEnv("TEMPORAL_TASKQUEUE", "agent-sdk-go"),
		LogLevel:  getEnv("LOG_LEVEL", "error"),
		Provider:  interfaces.LLMProvider(getEnv("LLM_PROVIDER", "openai")),
		APIKey:    getEnv("LLM_APIKEY", ""),
		Model:     getEnv("LLM_MODEL", "gpt-4o"),
		BaseURL:   getEnv("LLM_BASEURL", ""),
	}
	return cfg
}

// NewLoggerFromLogConfig returns logger.Logger for use with the agent. Logs to stderr so
// conversation (stdout) stays separate; set LOG_LEVEL=info or debug to see logs.
func NewLoggerFromLogConfig(cfg *Config) logger.Logger {
	level := "error"
	if cfg != nil && cfg.LogLevel != "" {
		level = strings.TrimSpace(cfg.LogLevel)
	}
	return logger.DefaultLogger(level)
}

// NewLLMClientFromConfig creates an LLM client from config using the new llm.Option-based API.
// BaseURL is applied only for OpenAI; set LLM_BASEURL when using a non-default OpenAI-compatible API.
func NewLLMClientFromConfig(cfg *Config) (interfaces.LLMClient, error) {
	opts := []llm.Option{
		llm.WithAPIKey(cfg.APIKey),
		llm.WithModel(cfg.Model),
		llm.WithLogger(NewLoggerFromLogConfig(cfg)),
	}
	switch cfg.Provider {
	case interfaces.LLMProviderAnthropic:
		return anthropic.NewClient(opts...)
	case interfaces.LLMProviderOpenAI:
		if cfg.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.BaseURL))
		}
		return openai.NewClient(opts...)
	case interfaces.LLMProviderGemini:
		return gemini.NewClient(opts...)
	default:
		if cfg.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.BaseURL))
		}
		return openai.NewClient(opts...)
	}
}
