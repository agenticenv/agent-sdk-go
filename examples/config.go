package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/agent-sdk-go/pkg/llm"
	"github.com/vvsynapse/agent-sdk-go/pkg/llm/anthropic"
	"github.com/vvsynapse/agent-sdk-go/pkg/llm/gemini"
	"github.com/vvsynapse/agent-sdk-go/pkg/llm/openai"
	"github.com/vvsynapse/agent-sdk-go/pkg/logger"
	"go.temporal.io/sdk/log"
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
	BaseURL   string
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
	}
	return cfg
}

// NewLoggerFromLogConfig returns a log.Logger for use with the agent. Logs to stderr so
// conversation (stdout) stays separate; set LOG_LEVEL=info or debug to see logs.
func NewLoggerFromLogConfig(cfg *Config) log.Logger {
	level := "error"
	if cfg != nil && cfg.LogLevel != "" {
		level = strings.TrimSpace(cfg.LogLevel)
	}
	return logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{
		Level:  level,
		Output: "stderr",
	}))
}

// NewLLMClientFromConfig creates an LLM client from config using the new llm.Option-based API.
func NewLLMClientFromConfig(cfg *Config) (interfaces.LLMClient, error) {
	opts := []llm.Option{
		llm.WithAPIKey(cfg.APIKey),
		llm.WithModel(cfg.Model),
		llm.WithBaseURL(cfg.BaseURL),
		llm.WithLogger(NewLoggerFromLogConfig(cfg)),
	}
	switch cfg.Provider {
	case interfaces.LLMProviderAnthropic:
		return anthropic.NewClient(opts...)
	case interfaces.LLMProviderOpenAI:
		return openai.NewClient(opts...)
	case interfaces.LLMProviderGemini:
		return gemini.NewClient(opts...)
	default:
		return openai.NewClient(opts...)
	}
}
