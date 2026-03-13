package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/vinodvanja/temporal-agents-go/pkg/llm"
	"github.com/vinodvanja/temporal-agents-go/pkg/logger"
	"go.temporal.io/sdk/log"
)

type Config struct {
	Temporal *TemporalConfig
	LLM      *LLMConfig
	Log      *LogConfig
}

// LogConfig holds logging settings. Default level "error" for minimal output.
type LogConfig struct {
	Level string
}

type TemporalConfig struct {
	Host      string
	Port      int
	Namespace string
	TaskQueue string // used as prefix; SDK derives unique queues per agent
}

type LLMConfig struct {
	Type    llm.LLMType
	APIKey  string
	Model   string
	BaseURL string
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
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
	_ = godotenv.Load("../.env")
}

// LoadFromEnv loads config from environment variables. .env is loaded on package init if present.
func LoadFromEnv() *Config {
	cfg := &Config{
		Temporal: &TemporalConfig{
			Host:      getEnv("TEMPORAL_HOST", "localhost"),
			Port:      getEnvInt("TEMPORAL_PORT", 7233),
			Namespace: getEnv("TEMPORAL_NAMESPACE", "default"),
			TaskQueue: getEnv("TEMPORAL_TASKQUEUE", "temporal-agents-go"),
		},
		LLM: &LLMConfig{
			Type:    llm.LLMType(getEnv("LLM_TYPE", "openai")),
			APIKey:  getEnv("LLM_APIKEY", ""),
			Model:   getEnv("LLM_MODEL", "gpt-4o"),
			BaseURL: getEnv("LLM_BASEURL", "https://api.openai.com/v1"),
		},
	}
	cfg.Log = &LogConfig{
		Level: getEnv("LOG_LEVEL", "error"),
	}
	return cfg
}

// NewLoggerFromLogConfig returns a log.Logger for use with the agent.
func NewLoggerFromLogConfig(cfg *LogConfig) log.Logger {
	level := "error"
	if cfg != nil && cfg.Level != "" {
		level = strings.TrimSpace(cfg.Level)
	}
	return logger.NewZapAdapter(logger.NewZapLoggerWithLevel(level))
}
