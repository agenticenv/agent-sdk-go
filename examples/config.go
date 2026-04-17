package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/anthropic"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/gemini"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/openai"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/mcp"
	"github.com/joho/godotenv"
	"golang.org/x/oauth2/clientcredentials"
)

// MCPSettings holds MCP_* environment values for agent_with_mcp_* examples.
// This is not [github.com/agenticenv/agent-sdk-go/pkg/agent.MCPConfig] (per-server agent transport + filter).
type MCPSettings struct {
	// Transport is required for MCPLoadTransport: stdio or streamable_http (aliases: local; http, remote, streamable).
	Transport string
	// StreamableHTTPURL is the remote MCP endpoint when transport is streamable_http.
	StreamableHTTPURL string
	// StdioCommand is the executable for MCP stdio transport (required when transport is stdio).
	StdioCommand string
	// StdioArgsRaw is JSON array of strings for subprocess argv, e.g. ["-y","@scope/mcp-server","/data"].
	StdioArgsRaw string
	// StdioEnvRaw is JSON object of extra env vars for the subprocess, e.g. {"API_KEY":"..."}.
	StdioEnvRaw string
	// BearerToken is an optional static bearer for MCP HTTP. Ignored when OAuth env trio is all set.
	BearerToken string
	// Name is the stable server id for this MCP connection (empty defaults to local for stdio, remote for HTTP).
	Name string
	// RetryAttempts is max connect+RPC attempts per operation when > 0; zero uses SDK default.
	RetryAttempts int
	// AllowTools is comma-separated tool names to allow-list (optional); mutually exclusive with BlockTools in validation.
	AllowTools string
	// BlockTools is comma-separated tool names to block-list (optional).
	BlockTools string
	// TimeoutSeconds caps each MCP connect+RPC attempt when > 0 (seconds). Zero uses SDK defaults.
	TimeoutSeconds int
}

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

	MCP MCPSettings
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
	err := godotenv.Load(".env")
	if err != nil {
		_ = godotenv.Load("./examples/.env")
	}
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
		MCP: MCPSettings{
			Transport:         strings.TrimSpace(strings.ToLower(getEnv("MCP_TRANSPORT", ""))),
			StreamableHTTPURL: strings.TrimSpace(getEnv("MCP_STREAMABLE_HTTP_URL", "")),
			StdioCommand:      strings.TrimSpace(getEnv("MCP_STDIO_COMMAND", "")),
			StdioArgsRaw:      strings.TrimSpace(getEnv("MCP_STDIO_ARGS", "")),
			StdioEnvRaw:       strings.TrimSpace(getEnv("MCP_STDIO_ENV", "")),
			BearerToken:       strings.TrimSpace(getEnv("MCP_BEARER_TOKEN", "")),
			Name:              strings.TrimSpace(getEnv("MCP_SERVER_NAME", "")),
			RetryAttempts:     getEnvInt("MCP_RETRY_ATTEMPTS", 0),
			AllowTools:        strings.TrimSpace(getEnv("MCP_ALLOW_TOOLS", "")),
			BlockTools:        strings.TrimSpace(getEnv("MCP_BLOCK_TOOLS", "")),
			TimeoutSeconds:    getEnvInt("MCP_TIMEOUT_SECONDS", 0),
		},
	}
	return cfg
}

// MCPUsesOAuthFromEnv reports whether OAuth2 client-credentials env vars are all non-empty.
func MCPUsesOAuthFromEnv() bool {
	return strings.TrimSpace(os.Getenv("MCP_CLIENT_ID")) != "" &&
		strings.TrimSpace(os.Getenv("MCP_CLIENT_SECRET")) != "" &&
		strings.TrimSpace(os.Getenv("MCP_TOKEN_URL")) != ""
}

// ApplyMCPStreamableHTTPAuth sets optional auth on transport from m and process env. Unauthenticated MCP (URL only) is valid.
// When the OAuth trio is set, OAuth is used; otherwise m.BearerToken sets a bearer token when non-empty.
// MCP_SKIP_TLS_VERIFY=true sets SkipTLSVerify.
func ApplyMCPStreamableHTTPAuth(transport *mcp.MCPStreamableHTTP, m *MCPSettings) {
	if transport == nil {
		return
	}
	if MCPUsesOAuthFromEnv() {
		transport.OAuthClientCreds = &clientcredentials.Config{
			ClientID:     strings.TrimSpace(os.Getenv("MCP_CLIENT_ID")),
			ClientSecret: strings.TrimSpace(os.Getenv("MCP_CLIENT_SECRET")),
			TokenURL:     strings.TrimSpace(os.Getenv("MCP_TOKEN_URL")),
		}
	} else if m != nil && m.BearerToken != "" {
		transport.Token = m.BearerToken
	}
	if strings.TrimSpace(os.Getenv("MCP_SKIP_TLS_VERIFY")) == "true" {
		transport.SkipTLSVerify = true
	}
}

// normalizeMCPTransport maps MCP_TRANSPORT (required) to canonical stdio or streamable_http.
func normalizeMCPTransport(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", fmt.Errorf("mcp: MCP_TRANSPORT is required (stdio or streamable_http); see examples/env.sample")
	}
	switch s {
	case "stdio", "local":
		return "stdio", nil
	case "streamable_http", "http", "remote", "streamable":
		return "streamable_http", nil
	default:
		return "", fmt.Errorf("mcp: unknown MCP_TRANSPORT %q (use stdio or streamable_http)", strings.TrimSpace(raw))
	}
}

// MCPLoadTransport builds mcp.MCPStdio or mcp.MCPStreamableHTTP from cfg and process env.
// streamable_http requires MCP_STREAMABLE_HTTP_URL. stdio requires MCP_STDIO_COMMAND; optional MCP_STDIO_ARGS (JSON array) and MCP_STDIO_ENV (JSON object).
func MCPLoadTransport(cfg *Config) (mcp.MCPTransportConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("mcp: config is nil")
	}
	kind, err := normalizeMCPTransport(cfg.MCP.Transport)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "stdio":
		if cfg.MCP.StdioCommand == "" {
			return nil, fmt.Errorf("mcp: MCP_STDIO_COMMAND is required when MCP_TRANSPORT is stdio")
		}
		args, err := parseMCPJSONStringSlice(cfg.MCP.StdioArgsRaw)
		if err != nil {
			return nil, fmt.Errorf("mcp: MCP_STDIO_ARGS must be a JSON array of strings: %w", err)
		}
		env, err := parseMCPJSONStringMap(cfg.MCP.StdioEnvRaw)
		if err != nil {
			return nil, fmt.Errorf("mcp: MCP_STDIO_ENV must be a JSON object with string values: %w", err)
		}
		s := mcp.MCPStdio{Command: cfg.MCP.StdioCommand, Args: args, Env: env}
		return s, s.Validate()
	case "streamable_http":
		if cfg.MCP.StreamableHTTPURL == "" {
			return nil, fmt.Errorf("mcp: MCP_STREAMABLE_HTTP_URL is required when MCP_TRANSPORT is streamable_http")
		}
		t := mcp.MCPStreamableHTTP{URL: cfg.MCP.StreamableHTTPURL}
		ApplyMCPStreamableHTTPAuth(&t, &cfg.MCP)
		return t, t.Validate()
	default:
		return nil, fmt.Errorf("mcp: internal transport kind %q", kind)
	}
}

// MCPDefaultServerName returns MCP_SERVER_NAME or a default from transport (local / remote).
func MCPDefaultServerName(cfg *Config) string {
	if cfg != nil && cfg.MCP.Name != "" {
		return cfg.MCP.Name
	}
	kind, err := normalizeMCPTransport(cfg.MCP.Transport)
	if err != nil {
		return "remote"
	}
	if kind == "stdio" {
		return "local"
	}
	return "remote"
}

// MCPToolFilterFromConfig returns allow/block lists from comma-separated MCP_ALLOW_TOOLS / MCP_BLOCK_TOOLS.
func MCPToolFilterFromConfig(cfg *Config) (mcp.MCPToolFilter, error) {
	if cfg == nil {
		return mcp.MCPToolFilter{}, nil
	}
	f := mcp.MCPToolFilter{
		AllowTools: splitCommaNonEmpty(cfg.MCP.AllowTools),
		BlockTools: splitCommaNonEmpty(cfg.MCP.BlockTools),
	}
	if err := f.Validate(); err != nil {
		return mcp.MCPToolFilter{}, err
	}
	return f, nil
}

func splitCommaNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseMCPJSONStringSlice(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseMCPJSONStringMap(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MCPTimeout returns cfg.MCP.TimeoutSeconds as a duration, or zero if unset (applies to any MCP transport).
func (cfg *Config) MCPTimeout() time.Duration {
	if cfg == nil || cfg.MCP.TimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.MCP.TimeoutSeconds) * time.Second
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

const repoTemporalSetupDoc = "temporal-setup.md"

// FormatNewAgentError formats errors from [agent.NewAgent] or [agent.NewAgentWorker] for log output
// when running examples from a clone of this repository. It appends a pointer to temporal-setup.md
// when the failure is a Temporal connection or namespace timeout from this SDK.
func FormatNewAgentError(prefix string, err error) string {
	if err == nil {
		return ""
	}
	msg := fmt.Sprintf("%s: %v", prefix, err)
	if errors.Is(err, types.ErrTemporalDialTimeout) || errors.Is(err, types.ErrTemporalNamespaceCheckTimeout) {
		msg += "\n\nFor a local Temporal dev server, see " + repoTemporalSetupDoc + " at the repository root."
	}
	return msg
}
