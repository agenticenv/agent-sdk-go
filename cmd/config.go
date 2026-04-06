package main

import (
	"fmt"
	"io"
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
	// BaseURL is optional; only used when provider is openai (custom/Azure-compatible API).
	BaseURL string `mapstructure:"baseURL"`
}

type LoggerConfig struct {
	Level     string `mapstructure:"level"`
	Output    string `mapstructure:"output"`     // stdout | stderr | file path; default resolves to cmd/logs/agent.log
	Format    string `mapstructure:"format"`     // text | json (file/stdout/stderr)
	AddSource bool   `mapstructure:"add_source"` // include file:line in each log line (slog source)
	TeeStderr bool   `mapstructure:"tee_stderr"` // when output is a file, also copy to stderr (usually off so the REPL stays clean)
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
	_ = v.BindEnv("logger.format", "AGENT_LOGGER_FORMAT")
	_ = v.BindEnv("logger.add_source", "AGENT_LOGGER_ADD_SOURCE")
	_ = v.BindEnv("logger.tee_stderr", "AGENT_LOGGER_TEE_STDERR")

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
	v.SetDefault("logger.format", "json")
	v.SetDefault("logger.add_source", true)
	v.SetDefault("logger.tee_stderr", false)

	_ = v.ReadInConfig() // ignore: file not found uses defaults + env
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	if cfg.Logger != nil {
		// Viper defaults for bool can unmarshal as false when the key is absent; match SetDefault intent.
		if !v.IsSet("logger.add_source") {
			cfg.Logger.AddSource = true
		}
		if !v.IsSet("logger.format") {
			cfg.Logger.Format = "json"
		}
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
func NewLLMClient(cfg *Config, lgr logger.Logger) (interfaces.LLMClient, error) {
	if cfg == nil || cfg.LLM == nil {
		return nil, fmt.Errorf("LLM config is required")
	}
	if lgr == nil {
		lgr = newLogger(cfg.Logger)
	}
	opts := []llm.Option{
		llm.WithAPIKey(cfg.LLM.APIKey),
		llm.WithModel(cfg.LLM.Model),
		llm.WithLogger(lgr),
		llm.WithLogLevel(getLogLevel(cfg.Logger)),
	}
	switch interfaces.LLMProvider(cfg.LLM.Provider) {
	case interfaces.LLMProviderAnthropic:
		return anthropic.NewClient(opts...)
	case interfaces.LLMProviderOpenAI:
		if cfg.LLM.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.LLM.BaseURL))
		}
		return openai.NewClient(opts...)
	case interfaces.LLMProviderGemini:
		return gemini.NewClient(opts...)
	default:
		if cfg.LLM.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.LLM.BaseURL))
		}
		return openai.NewClient(opts...)
	}
}

func newLogger(cfg *LoggerConfig) logger.Logger {
	level := getLogLevel(cfg)
	format := getLogFormat(cfg)
	addSource := getLogAddSource(cfg)

	if cfg != nil {
		switch strings.ToLower(strings.TrimSpace(cfg.Output)) {
		case "stdout":
			return logger.NewWriterLogger(os.Stdout, level, format, addSource)
		case "stderr":
			return logger.NewWriterLogger(os.Stderr, level, format, addSource)
		}
	}
	path := getLogOutput(cfg)
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return logger.DefaultLogger(level)
	}
	var w io.Writer = f
	if cfg != nil && cfg.TeeStderr {
		w = io.MultiWriter(f, os.Stderr)
	}
	return logger.NewWriterLogger(w, level, format, addSource)
}

func getLogFormat(cfg *LoggerConfig) string {
	if cfg != nil && strings.TrimSpace(cfg.Format) != "" {
		return strings.TrimSpace(cfg.Format)
	}
	return "json"
}

func getLogAddSource(cfg *LoggerConfig) bool {
	if cfg == nil {
		return true
	}
	return cfg.AddSource
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
