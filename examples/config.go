package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/llm"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/anthropic"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/deepseek"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/gemini"
	"github.com/agenticenv/agent-sdk-go/pkg/llm/ollama"
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
	// AgentRuntime is "local" (default) or "temporal", loaded from AGENT_RUNTIME.
	// Use TemporalOption(cfg) in examples instead of hardcoding agent.WithTemporalConfig so
	// the runtime can be toggled without removing Temporal env vars.
	AgentRuntime string

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

	// A2A holds A2A_* environment values for agent_with_a2a_* examples.
	A2A A2ASettings

	// A2AServer holds A2A_SERVER_* values for agent_with_a2a_server (inbound HTTP server).
	A2AServer A2AServerEnv
}

// A2AServerEnv configures the built-in A2A HTTP server (listen address and optional bearer tokens).
// Used by [A2AInboundServerOption] and [A2AServerDisplayURL].
type A2AServerEnv struct {
	// Hostname is the bind address (empty with Port 0 and no tokens → use SDK defaults via [agent.WithA2ADefaultServer]).
	Hostname string
	// Port is the TCP listen port (0 means default 9999 when combined with [agent.WithA2AServer]).
	Port int
	// BearerTokens are accepted static Bearer tokens for JSON-RPC (comma-separated in env).
	BearerTokens []string
}

// A2ASettings holds env-driven settings for wiring [agent.WithA2AConfig] or [pkg/a2a/client.NewClient].
type A2ASettings struct {
	// URL is the A2A agent base URL for card resolution (required for the A2A examples).
	URL string
	// Name is the stable connection id used as the server key in tool names (default: remote).
	Name string
	// TimeoutSeconds caps each A2A HTTP operation when > 0; zero uses the SDK default.
	TimeoutSeconds int
	// Token is an optional bearer token (Authorization: Bearer ...).
	Token string
	// HeadersRaw is optional JSON object of extra HTTP headers, e.g. {"X-Api-Key":"..."}.
	HeadersRaw string
	// SkipTLSVerify disables TLS verification for the A2A client (development only).
	SkipTLSVerify bool
	// AllowSkills is comma-separated skill IDs to expose (mutually exclusive with BlockSkills).
	AllowSkills string
	// BlockSkills is comma-separated skill IDs to hide (mutually exclusive with AllowSkills).
	BlockSkills string
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

// ToolApprovalOptions applies AutoToolApprovalPolicy when EXAMPLES_AUTO_APPROVE=true
// (task batch runs). Manual go run leaves it unset or false (default require-all + prompts).
func ToolApprovalOptions() []agent.Option {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("EXAMPLES_AUTO_APPROVE")), "true") {
		return []agent.Option{agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy())}
	}
	return nil
}

func init() {
	loadEnvFiles()
}

// loadEnvFiles loads .env.defaults then optional .env under examples/ (cwd or ./examples/).
// Load order: defaults → .env overrides → process environment (export / Task / CI) wins.
func loadEnvFiles() {
	pairs := [][2]string{
		{".env.defaults", ".env"},
		{"./examples/.env.defaults", "./examples/.env"},
	}
	for _, pair := range pairs {
		defaultsPath, envPath := pair[0], pair[1]
		if _, err := os.Stat(defaultsPath); err != nil {
			continue
		}
		applyEnvFiles(defaultsPath, envPath)
		return
	}
}

func applyEnvFiles(defaultsPath, envPath string) {
	defaults, err := godotenv.Read(defaultsPath)
	if err != nil {
		return
	}
	merged := defaults
	if env, err := godotenv.Read(envPath); err == nil {
		for k, v := range env {
			merged[k] = v
		}
	}
	for k, v := range merged {
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}

func defaultTaskQueue() string {
	const base = "agent-sdk-go"
	// Derive a stable per-example queue suffix from the first path segment after ".../examples/".
	// This makes agent/worker pairs under the same example (e.g. durable_agent/agent, durable_agent/worker)
	// share a queue while different examples do not collide when run in parallel.
	suffix := ""
	pcs := make([]uintptr, 16)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		file := filepath.ToSlash(f.File)
		if idx := strings.Index(file, "/examples/"); idx >= 0 {
			rest := strings.TrimPrefix(file[idx+len("/examples/"):], "/")
			if rest != "" {
				parts := strings.Split(rest, "/")
				if len(parts) > 0 && parts[0] != "" {
					name := strings.ToLower(strings.TrimSpace(parts[0]))
					// Skip this config package file frame (examples/config.go) and continue
					// to the caller frame from the real example main package.
					if strings.HasSuffix(name, ".go") {
						if !more {
							break
						}
						continue
					}
					name = strings.NewReplacer(" ", "-", "_", "-", "/", "-", "\\", "-").Replace(name)
					name = strings.Trim(name, "-")
					if name != "" {
						suffix = name
						break
					}
				}
			}
		}
		if !more {
			break
		}
	}

	// TEMPORAL_TASKQUEUE acts as a base prefix; append per-example suffix automatically.
	// This preserves easy global overrides while keeping examples isolated by default.
	prefix := strings.TrimSpace(os.Getenv("TEMPORAL_TASKQUEUE"))
	if prefix == "" {
		prefix = base
	}
	if suffix == "" {
		return prefix
	}
	if strings.HasSuffix(prefix, "-"+suffix) {
		return prefix
	}
	return prefix + "-" + suffix
}

// LoadFromEnv loads config from environment variables. .env.defaults and optional .env are loaded on package init.
func LoadFromEnv() *Config {
	cfg := &Config{
		AgentRuntime: strings.ToLower(strings.TrimSpace(getEnv("AGENT_RUNTIME", "local"))),
		Host:         getEnv("TEMPORAL_HOST", "localhost"),
		Port:         getEnvInt("TEMPORAL_PORT", 7233),
		Namespace:    getEnv("TEMPORAL_NAMESPACE", "default"),
		TaskQueue:    defaultTaskQueue(),
		LogLevel:     getEnv("LOG_LEVEL", "error"),
		Provider:     interfaces.LLMProvider(getEnv("LLM_PROVIDER", "openai")),
		APIKey:       getEnv("LLM_APIKEY", ""),
		Model:        getEnv("LLM_MODEL", "gpt-4o"),
		BaseURL:      getEnv("LLM_BASEURL", ""),
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
		A2A: A2ASettings{
			URL:            strings.TrimSpace(getEnv("A2A_URL", "")),
			Name:           strings.TrimSpace(getEnv("A2A_SERVER_NAME", "")),
			TimeoutSeconds: getEnvInt("A2A_TIMEOUT_SECONDS", 0),
			Token:          strings.TrimSpace(getEnv("A2A_TOKEN", "")),
			HeadersRaw:     strings.TrimSpace(getEnv("A2A_HEADERS", "")),
			SkipTLSVerify:  strings.TrimSpace(getEnv("A2A_SKIP_TLS_VERIFY", "")) == "true",
			AllowSkills:    strings.TrimSpace(getEnv("A2A_ALLOW_SKILLS", "")),
			BlockSkills:    strings.TrimSpace(getEnv("A2A_BLOCK_SKILLS", "")),
		},
		A2AServer: A2AServerEnv{
			Hostname:     strings.TrimSpace(getEnv("A2A_SERVER_HOST", "")),
			Port:         getEnvInt("A2A_SERVER_PORT", 0),
			BearerTokens: splitCommaNonEmpty(strings.TrimSpace(getEnv("A2A_SERVER_BEARER_TOKENS", ""))),
		},
	}
	return cfg
}

// UseTemporalRuntime reports whether AGENT_RUNTIME is set to "temporal".
func (c *Config) UseTemporalRuntime() bool {
	return c != nil && c.AgentRuntime == "temporal"
}

// RuntimeOption returns [agent.WithTemporalConfig] when AGENT_RUNTIME=temporal, or nil for
// the local runtime. Spread into the options slice:
//
//	opts = append(opts, config.RuntimeOption(cfg)...)
//
// This keeps examples runtime-agnostic: toggle via AGENT_RUNTIME without touching code.
// If you need to hard-code a specific runtime in a single example, skip this helper and
// call [agent.WithTemporalConfig] (or nothing) directly.
func RuntimeOption(cfg *Config) []agent.Option {
	if cfg == nil || !cfg.UseTemporalRuntime() {
		return nil
	}
	return []agent.Option{
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
	}
}

const (
	defaultA2AServerDisplayHost = "localhost"
	defaultA2AServerDisplayPort = 9999
)

// A2AInboundServerOption returns [agent.WithA2ADefaultServer] when no custom listen address or
// tokens are set; otherwise [agent.WithA2AServer] with hostname/port defaults applied by the agent.
func A2AInboundServerOption(cfg *Config) agent.Option {
	if cfg == nil {
		return agent.WithA2ADefaultServer()
	}
	h := strings.TrimSpace(cfg.A2AServer.Hostname)
	p := cfg.A2AServer.Port
	toks := cfg.A2AServer.BearerTokens
	if h == "" && p == 0 && len(toks) == 0 {
		return agent.WithA2ADefaultServer()
	}
	return agent.WithA2AServer(&agent.A2AServerConfig{
		Hostname:     h,
		Port:         p,
		BearerTokens: toks,
	})
}

// A2AServerDisplayURL returns the agent base URL (scheme + host + port) for logs and docs,
// using the same defaults as the SDK when env leaves host/port unset.
func A2AServerDisplayURL(cfg *Config) string {
	host := defaultA2AServerDisplayHost
	port := defaultA2AServerDisplayPort
	if cfg != nil {
		if x := strings.TrimSpace(cfg.A2AServer.Hostname); x != "" {
			host = x
		}
		if cfg.A2AServer.Port != 0 {
			port = cfg.A2AServer.Port
		}
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// A2ATimeout returns cfg.A2A.TimeoutSeconds as a duration, or zero if unset.
func (cfg *Config) A2ATimeout() time.Duration {
	if cfg == nil || cfg.A2A.TimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.A2A.TimeoutSeconds) * time.Second
}

// A2ADefaultServerName returns A2A_SERVER_NAME or "remote".
func A2ADefaultServerName(cfg *Config) string {
	if cfg != nil && cfg.A2A.Name != "" {
		return cfg.A2A.Name
	}
	return "remote"
}

// A2ABuildAgentConfig builds [agent.A2AConfig] from env. Requires non-empty A2A_URL.
func A2ABuildAgentConfig(cfg *Config) (agent.A2AConfig, error) {
	if cfg == nil || strings.TrimSpace(cfg.A2A.URL) == "" {
		return agent.A2AConfig{}, fmt.Errorf("a2a: A2A_URL is required; see examples/.env.defaults or examples/README.md#env-vars")
	}
	hdrs, err := parseMCPJSONStringMap(cfg.A2A.HeadersRaw)
	if err != nil {
		return agent.A2AConfig{}, fmt.Errorf("a2a: A2A_HEADERS must be a JSON object with string values: %w", err)
	}
	sf := types.A2ASkillFilter{
		AllowSkills: splitCommaNonEmpty(cfg.A2A.AllowSkills),
		BlockSkills: splitCommaNonEmpty(cfg.A2A.BlockSkills),
	}
	if err := sf.Validate(); err != nil {
		return agent.A2AConfig{}, err
	}
	return agent.A2AConfig{
		URL:           strings.TrimSpace(cfg.A2A.URL),
		Timeout:       cfg.A2ATimeout(),
		Token:         strings.TrimSpace(cfg.A2A.Token),
		Headers:       hdrs,
		SkillFilter:   sf,
		SkipTLSVerify: cfg.A2A.SkipTLSVerify,
	}, nil
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
		return "", fmt.Errorf("mcp: MCP_TRANSPORT is required (stdio or streamable_http); see examples/.env.defaults or examples/README.md#env-vars")
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
// BaseURL applies to the OpenAI, DeepSeek, and Ollama providers; set LLM_BASEURL for a
// non-default OpenAI-compatible endpoint. DeepSeek and Ollama default to their own endpoints
// when LLM_BASEURL is unset (Ollama to http://localhost:11434/v1, requiring no API key).
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
	case interfaces.LLMProviderDeepSeek:
		if cfg.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.BaseURL))
		}
		return deepseek.NewClient(opts...)
	case interfaces.LLMProviderOllama:
		// Ollama needs no API key; its client defaults BaseURL to http://localhost:11434/v1.
		if cfg.BaseURL != "" {
			opts = append(opts, llm.WithBaseURL(cfg.BaseURL))
		}
		return ollama.NewClient(opts...)
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
