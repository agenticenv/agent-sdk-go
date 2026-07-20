package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/anthropic"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/deepseek"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/gemini"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/ollama"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/openai"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

// NewLLMClient creates an LLM client from config using pkg/llm options.
// cfg.LLM.APIKey is expected to already be resolved (see LoadConfig).
func NewLLMClient(cfg *Config, lgr logger.Logger) (interfaces.LLMClient, error) {
	if cfg == nil || cfg.LLM == nil {
		return nil, fmt.Errorf("LLM config is required")
	}
	if lgr == nil {
		lgr = NewLogger(cfg.Logger)
	}
	opts := []llm.Option{
		llm.WithAPIKey(cfg.LLM.APIKey),
		llm.WithModel(cfg.LLM.Model),
		llm.WithLogger(lgr),
		llm.WithLogLevel(getLogLevel(cfg.Logger)),
	}
	switch interfaces.LLMProvider(cfg.LLM.Provider) {
	case interfaces.LLMProviderAnthropic:
		opts = append(opts, llm.WithPromptCaching(true))
		return anthropic.NewClient(opts...)
	case interfaces.LLMProviderOpenAI:
		if cfg.LLM.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.LLM.BaseURL))
		}
		return openai.NewClient(opts...)
	case interfaces.LLMProviderGemini:
		return gemini.NewClient(opts...)
	case interfaces.LLMProviderDeepSeek:
		if cfg.LLM.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.LLM.BaseURL))
		}
		return deepseek.NewClient(opts...)
	case interfaces.LLMProviderOllama:
		if cfg.LLM.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.LLM.BaseURL))
		}
		return ollama.NewClient(opts...)
	default:
		if cfg.LLM.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.LLM.BaseURL))
		}
		return openai.NewClient(opts...)
	}
}

// NewLogger builds a logger.Logger from LoggerConfig, defaulting to a file
// under the project root (dev) or XDG state home (installed).
func NewLogger(cfg *LoggerConfig) logger.Logger {
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
	if cfg == nil || cfg.AddSource == nil {
		return true
	}
	return *cfg.AddSource
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
	if output == "" || output == "logs/agctl.log" {
		// Prefer cli module root when developing from source; else XDG state home.
		if root := findProjectRoot(); root != "" {
			output = filepath.Join(root, "logs", "agctl.log")
		} else if path, err := xdg.StateFile(defaultLogRelPath); err == nil {
			output = path
		} else {
			output = filepath.Join(xdg.StateHome, defaultLogRelPath)
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
