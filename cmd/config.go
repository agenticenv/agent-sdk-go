package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/anthropic"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/gemini"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/openai"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/log"
)

type Config struct {
	Temporal *TemporalConfig `mapstructure:"temporal"`
	LLM      *LLMConfig      `mapstructure:"llm"`
	Logger   *LoggerConfig   `mapstructure:"logger"`
}

type TemporalConfig struct {
	Host      string `mapstructure:"host"`
	Port      int    `mapstructure:"port"`
	Namespace string `mapstructure:"namespace"`
	TaskQueue string `mapstructure:"taskQueue"`
}

type LLMConfig struct {
	Provider string `mapstructure:"provider"`
	APIKey   string `mapstructure:"apiKey"`
	Model    string `mapstructure:"model"`
	BaseURL  string `mapstructure:"baseURL"`
}

type LoggerConfig struct {
	Level  string `mapstructure:"level"`
	Output string `mapstructure:"output"` // file path for logs (e.g. cmd/logs/agent.log); empty = stderr
}

// LoadConfig loads config from file (YAML). Env vars with AGENT_ prefix override file values.
// Env keys: AGENT_TEMPORAL_HOST, AGENT_LLM_APIKEY, AGENT_LOGGER_LEVEL, etc.
func LoadConfig(path string) (*Config, error) {
	v := viper.New()
	if path == "" {
		path = "cmd/config.yaml"
	}
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("AGENT")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Explicit BindEnv so AGENT_* env vars reliably override (AutomaticEnv can be inconsistent with nested keys)
	_ = v.BindEnv("temporal.host", "AGENT_TEMPORAL_HOST")
	_ = v.BindEnv("temporal.port", "AGENT_TEMPORAL_PORT")
	_ = v.BindEnv("temporal.namespace", "AGENT_TEMPORAL_NAMESPACE")
	_ = v.BindEnv("temporal.taskQueue", "AGENT_TEMPORAL_TASKQUEUE")
	_ = v.BindEnv("llm.provider", "AGENT_LLM_PROVIDER")
	_ = v.BindEnv("llm.apiKey", "AGENT_LLM_APIKEY")
	_ = v.BindEnv("llm.model", "AGENT_LLM_MODEL")
	_ = v.BindEnv("llm.baseURL", "AGENT_LLM_BASEURL")
	_ = v.BindEnv("logger.level", "AGENT_LOGGER_LEVEL")
	_ = v.BindEnv("logger.output", "AGENT_LOGGER_OUTPUT")

	// Set defaults so env can override even when file is missing or key absent
	v.SetDefault("temporal.host", "localhost")
	v.SetDefault("temporal.port", 7233)
	v.SetDefault("temporal.namespace", "default")
	v.SetDefault("temporal.taskQueue", "agent-sdk-go")
	v.SetDefault("llm.provider", "openai")
	v.SetDefault("llm.model", "gpt-4o")
	v.SetDefault("llm.baseURL", "")
	v.SetDefault("logger.level", "error")
	v.SetDefault("logger.output", "logs/agent.log")

	_ = v.ReadInConfig() // ignore: file not found uses defaults + env
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	// Ensure nested structs are allocated
	if cfg.Temporal == nil {
		cfg.Temporal = &TemporalConfig{}
	}
	if cfg.LLM == nil {
		cfg.LLM = &LLMConfig{}
	}
	if cfg.Logger == nil {
		cfg.Logger = &LoggerConfig{}
	}
	return &cfg, nil
}

// NewLLMClient creates an LLM client from config using pkg/llm options.
// If lgr is nil, a new logger is created from cfg.Logger.
func NewLLMClient(cfg *Config, lgr log.Logger) (interfaces.LLMClient, error) {
	if cfg == nil || cfg.LLM == nil {
		return nil, fmt.Errorf("LLM config is required")
	}
	if lgr == nil {
		lgr = newLogger(cfg.Logger)
	}
	opts := []llm.Option{
		llm.WithAPIKey(cfg.LLM.APIKey),
		llm.WithModel(cfg.LLM.Model),
		llm.WithBaseURL(cfg.LLM.BaseURL),
		llm.WithLogger(lgr),
		llm.WithLogLevel(getLogLevel(cfg.Logger)),
	}
	switch interfaces.LLMProvider(cfg.LLM.Provider) {
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

func newLogger(cfg *LoggerConfig) log.Logger {
	output := getLogOutput(cfg)
	if output != "" && output != "stdout" && output != "stderr" {
		if dir := filepath.Dir(output); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
	}
	return logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{
		Level:  getLogLevel(cfg),
		Output: output,
	}))
}

func getLogLevel(cfg *LoggerConfig) string {
	if cfg != nil && cfg.Level != "" {
		return strings.TrimSpace(cfg.Level)
	}
	return "error"
}

func getLogOutput(cfg *LoggerConfig) string {
	output := ""
	if cfg != nil && cfg.Output != "" {
		output = strings.TrimSpace(cfg.Output)
	}
	if output == "" || output == "logs/agent.log" {
		// Default: resolve to cmd/logs/agent.log so logs stay inside cmd/ regardless of cwd
		if root := findProjectRoot(); root != "" {
			output = filepath.Join(root, "cmd", "logs", "agent.log")
		} else {
			output = filepath.Join("cmd", "logs", "agent.log")
		}
	}
	return output
}

// findProjectRoot walks up from cwd to find the dir containing go.mod.
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
