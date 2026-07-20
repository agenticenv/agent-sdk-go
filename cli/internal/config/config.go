// Package config loads and resolves agctl's configuration.
//
// Precedence (later wins):
//  1. embedded YAML (compiled in; same bytes as default.yaml)
//  2. XDG config.yaml (after agctl config edit / first save), if present
//  3. --config / AGCTL_CONFIG file (partial override OK), if set and present
//  4. AGCTL_* environment variables
//  5. CLI flags (applied by callers via ApplyAgentOverrides)
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/adrg/xdg"
	"github.com/agenticenv/agent-sdk-go/cli/internal/secrets"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"gopkg.in/yaml.v3"
)

// EnvPrefix is the environment variable prefix for config overrides.
const EnvPrefix = "AGCTL_"

// Config is the root agctl configuration.
type Config struct {
	// Runtime selects the agent execution backend: "local" (default) or "temporal".
	Runtime      string                `yaml:"runtime"`
	Name         string                `yaml:"name"`
	SystemPrompt string                `yaml:"system_prompt"`
	Temporal     *TemporalConfig       `yaml:"temporal,omitempty"`
	LLM          *LLMConfig            `yaml:"llm,omitempty"`
	Logger       *LoggerConfig         `yaml:"logger,omitempty"`
	Conversation *ConversationConfig   `yaml:"conversation,omitempty"`
	MCP          *MCPRootConfig        `yaml:"mcp,omitempty"`
	Tools        map[string]ToolConfig `yaml:"tools,omitempty"`
	LLMUsage     bool                  `yaml:"llm_usage"`
}

// ConversationConfig selects the conversation store (inmem or redis).
type ConversationConfig struct {
	Store           string       `yaml:"store"` // inmem | redis
	MaxSize         int          `yaml:"max_size"`
	Size            int          `yaml:"size"` // max messages fetched for LLM context
	SaveOnIteration bool         `yaml:"save_on_iteration"`
	Redis           *RedisConfig `yaml:"redis,omitempty"`
}

// RedisConfig is used when conversation.store is redis.
type RedisConfig struct {
	Addr      string `yaml:"addr"`
	Password  string `yaml:"password,omitempty"`
	DB        int    `yaml:"db"`
	KeyPrefix string `yaml:"key_prefix"`
	TTLHours  int    `yaml:"ttl_hours"`
}

// ToolConfig toggles a built-in CLI tool. Missing keys default to enabled.
type ToolConfig struct {
	Enabled bool `yaml:"enabled"`
}

// Built-in tool names (must match pkg/tools Name() and default.yaml).
const (
	ToolEcho        = "echo"
	ToolCurrentTime = "current_time"
	ToolRandom      = "random"
	ToolCalculator  = "calculator"
	ToolWeather     = "weather"
	ToolWikipedia   = "wikipedia"
	ToolSearch      = "search"
)

// BuiltinToolNames is the ordered list of built-in CLI tools.
var BuiltinToolNames = []string{
	ToolEcho,
	ToolCurrentTime,
	ToolRandom,
	ToolCalculator,
	ToolWeather,
	ToolWikipedia,
	ToolSearch,
}

// ToolEnabled reports whether the named built-in tool should be registered.
// Unknown / omitted tools default to true so partial YAML overrides stay safe.
func (c *Config) ToolEnabled(name string) bool {
	if c == nil || c.Tools == nil {
		return true
	}
	t, ok := c.Tools[name]
	if !ok {
		return true
	}
	return t.Enabled
}

type TemporalConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	Namespace string `yaml:"namespace"`
	TaskQueue string `yaml:"taskQueue"`
}

type LLMConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"apiKey,omitempty"`
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"baseURL,omitempty"`
}

type LoggerConfig struct {
	Level     string `yaml:"level"`
	Output    string `yaml:"output"`
	Format    string `yaml:"format"`
	AddSource *bool  `yaml:"add_source"`
	TeeStderr bool   `yaml:"tee_stderr"`
}

// UseTemporalRuntime reports whether the temporal backend is selected.
func (c *Config) UseTemporalRuntime() bool {
	return c != nil && strings.ToLower(strings.TrimSpace(c.Runtime)) == "temporal"
}

// RuntimeOption returns [agent.WithTemporalConfig] when runtime is "temporal", or nil for local.
func RuntimeOption(cfg *Config) []agent.Option {
	if !cfg.UseTemporalRuntime() || cfg.Temporal == nil {
		return nil
	}
	return []agent.Option{
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Temporal.Host,
			Port:      cfg.Temporal.Port,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}),
	}
}

const (
	appName           = "agctl"
	configFileRelPath = "agctl/config.yaml"
	defaultLogRelPath = "agctl/agctl.log"
)

// DefaultConfigDir returns the XDG config home for agctl ($XDG_CONFIG_HOME/agctl).
func DefaultConfigDir() string {
	return filepath.Join(xdg.ConfigHome, appName)
}

// DefaultConfigPath returns the preferred path for writing config.yaml
// ($XDG_CONFIG_HOME/agctl/config.yaml). Parent dirs are created if needed.
func DefaultConfigPath() string {
	path, err := xdg.ConfigFile(configFileRelPath)
	if err != nil {
		return filepath.Join(DefaultConfigDir(), "config.yaml")
	}
	return path
}

// XDGConfigPath returns the XDG config path without creating directories.
func XDGConfigPath() string {
	return filepath.Join(xdg.ConfigHome, configFileRelPath)
}

// ExplicitConfigPath returns --config or AGCTL_CONFIG if set (may not exist yet).
func ExplicitConfigPath(flagPath string) string {
	if p := strings.TrimSpace(flagPath); p != "" {
		return p
	}
	return strings.TrimSpace(os.Getenv("AGCTL_CONFIG"))
}

// ResolveConfigPath returns the path shown by `agctl config path`:
// explicit (--config / AGCTL_CONFIG) if set, otherwise the XDG path.
func ResolveConfigPath(explicit string) string {
	if p := ExplicitConfigPath(explicit); p != "" {
		return p
	}
	if p, err := xdg.SearchConfigFile(configFileRelPath); err == nil && FileExists(p) {
		return p
	}
	return XDGConfigPath()
}

// FileExists reports whether path exists and is a regular file.
func FileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// LoadConfig merges configuration layers and returns the effective config.
// embedded must be the compiled-in YAML (same as default.yaml / config edit seed).
//
// Layers: embedded → XDG (if present) → explicit file (if set and present) → AGCTL_* env.
// Callers apply CLI flags afterward via ApplyAgentOverrides.
func LoadConfig(explicitFlag string, embedded []byte) (*Config, error) {
	cfg := &Config{}
	if len(embedded) == 0 {
		return nil, fmt.Errorf("embedded config YAML is empty")
	}
	if err := yaml.Unmarshal(embedded, cfg); err != nil {
		return nil, fmt.Errorf("parse embedded config: %w", err)
	}
	ensureConfigStructs(cfg)

	merged := map[string]struct{}{}
	if err := mergeFileOnce(cfg, XDGConfigPath(), merged); err != nil {
		return nil, err
	}
	if explicit := ExplicitConfigPath(explicitFlag); explicit != "" {
		if !FileExists(explicit) {
			return nil, fmt.Errorf("config file %q not found", explicit)
		}
		if err := mergeFileOnce(cfg, explicit, merged); err != nil {
			return nil, err
		}
	}

	ensureConfigStructs(cfg)
	applyEnvOverrides(cfg)
	cfg.LLM.APIKey = secrets.ResolveAPIKey(cfg.LLM.APIKey)
	return cfg, nil
}

func mergeFileOnce(cfg *Config, path string, merged map[string]struct{}) error {
	if !FileExists(path) {
		return nil
	}
	key := path
	if abs, err := filepath.Abs(path); err == nil {
		key = abs
	}
	if _, ok := merged[key]; ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config %q: %w", path, err)
	}
	merged[key] = struct{}{}
	return nil
}

func ensureConfigStructs(cfg *Config) {
	if cfg.Temporal == nil {
		cfg.Temporal = &TemporalConfig{}
	}
	// Built-in defaults when temporal block is omitted from YAML (commented in default.yaml).
	if strings.TrimSpace(cfg.Temporal.Host) == "" {
		cfg.Temporal.Host = "localhost"
	}
	if cfg.Temporal.Port == 0 {
		cfg.Temporal.Port = 7233
	}
	if strings.TrimSpace(cfg.Temporal.Namespace) == "" {
		cfg.Temporal.Namespace = "default"
	}
	if strings.TrimSpace(cfg.Temporal.TaskQueue) == "" {
		cfg.Temporal.TaskQueue = "agent-sdk-go"
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "agctl"
	}
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		cfg.SystemPrompt = defaultSystemPrompt
	}
	if cfg.LLM == nil {
		cfg.LLM = &LLMConfig{}
	}
	if cfg.Logger == nil {
		cfg.Logger = &LoggerConfig{}
	}
	if cfg.Conversation == nil {
		cfg.Conversation = &ConversationConfig{}
	}
	if strings.TrimSpace(cfg.Conversation.Store) == "" {
		cfg.Conversation.Store = "inmem"
	}
	if cfg.Conversation.MaxSize <= 0 {
		cfg.Conversation.MaxSize = 100
	}
	if cfg.Conversation.Size <= 0 {
		cfg.Conversation.Size = 20
	}
	if cfg.Conversation.Redis == nil {
		cfg.Conversation.Redis = &RedisConfig{}
	}
	if strings.TrimSpace(cfg.Conversation.Redis.Addr) == "" {
		cfg.Conversation.Redis.Addr = "localhost:6379"
	}
	if strings.TrimSpace(cfg.Conversation.Redis.KeyPrefix) == "" {
		cfg.Conversation.Redis.KeyPrefix = "conversation"
	}
	if cfg.Conversation.Redis.TTLHours <= 0 {
		cfg.Conversation.Redis.TTLHours = 24
	}
	if cfg.MCP == nil {
		cfg.MCP = &MCPRootConfig{}
	}
	if cfg.Tools == nil {
		cfg.Tools = map[string]ToolConfig{}
	}
	// Fill omitted built-ins after partial merges (map replace) so show/load stay complete.
	for _, name := range BuiltinToolNames {
		if _, ok := cfg.Tools[name]; !ok {
			cfg.Tools[name] = ToolConfig{Enabled: true}
		}
	}
}

// defaultSystemPrompt is used when system_prompt is omitted from config.
const defaultSystemPrompt = "You are a helpful agent. Use the tools available to you whenever they improve accuracy—calculator for math, current_time for time, search, wikipedia, and weather for facts—and keep answers clear and concise."

func env(key string) string {
	return os.Getenv(EnvPrefix + key)
}

func applyEnvOverrides(cfg *Config) {
	if v := env("RUNTIME"); v != "" {
		cfg.Runtime = v
	}
	if v := env("NAME"); v != "" {
		cfg.Name = v
	}
	if v := env("SYSTEM_PROMPT"); v != "" {
		cfg.SystemPrompt = v
	}
	if v := env("CONVERSATION_STORE"); v != "" {
		cfg.Conversation.Store = v
	}
	if v := env("CONVERSATION_REDIS_ADDR"); v != "" {
		cfg.Conversation.Redis.Addr = v
	}
	if v := env("CONVERSATION_REDIS_PASSWORD"); v != "" {
		cfg.Conversation.Redis.Password = v
	}
	if v := env("CONVERSATION_REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Conversation.Redis.DB = n
		}
	}
	if v := env("TEMPORAL_HOST"); v != "" {
		cfg.Temporal.Host = v
	}
	if v := env("TEMPORAL_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Temporal.Port = n
		}
	}
	if v := env("TEMPORAL_NAMESPACE"); v != "" {
		cfg.Temporal.Namespace = v
	}
	if v := env("TEMPORAL_TASKQUEUE"); v != "" {
		cfg.Temporal.TaskQueue = v
	}
	if v := env("LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := env("LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := env("LLM_BASEURL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := env("LOGGER_LEVEL"); v != "" {
		cfg.Logger.Level = v
	}
	if v := env("LOGGER_OUTPUT"); v != "" {
		cfg.Logger.Output = v
	}
	if v := env("LOGGER_FORMAT"); v != "" {
		cfg.Logger.Format = v
	}
	if v := env("LOGGER_ADD_SOURCE"); v != "" {
		b := strings.EqualFold(v, "true") || v == "1"
		cfg.Logger.AddSource = &b
	}
	if v := env("LOGGER_TEE_STDERR"); v != "" {
		cfg.Logger.TeeStderr = strings.EqualFold(v, "true") || v == "1"
	}
	if v := env("LLM_USAGE"); v != "" {
		cfg.LLMUsage = strings.EqualFold(v, "true") || v == "1"
	}
}

// AgentOverrides are non-empty CLI/env values applied after LoadConfig
// (defaults→xdg→user-file→env already merged; these win as the flag layer).
type AgentOverrides struct {
	Runtime  string
	Provider string
	Model    string
	APIKey   string

	TemporalHost      string
	TemporalPort      int // 0 means unset
	TemporalNamespace string
	TemporalTaskQueue string

	LLMUsage bool // when true, enable usage summary for this invocation
}

// ApplyAgentOverrides applies non-empty chat/run flag overrides onto cfg.
// Temporal connection fields are applied whenever provided; when runtime is
// temporal and they are omitted, ensureConfigStructs defaults remain
// (localhost:7233 / default / agent-sdk-go).
func ApplyAgentOverrides(cfg *Config, o AgentOverrides) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(o.Runtime) != "" {
		cfg.Runtime = strings.TrimSpace(o.Runtime)
	}
	if cfg.LLM == nil {
		cfg.LLM = &LLMConfig{}
	}
	if strings.TrimSpace(o.Provider) != "" {
		cfg.LLM.Provider = strings.TrimSpace(o.Provider)
	}
	if strings.TrimSpace(o.Model) != "" {
		cfg.LLM.Model = strings.TrimSpace(o.Model)
	}
	if strings.TrimSpace(o.APIKey) != "" {
		cfg.LLM.APIKey = strings.TrimSpace(o.APIKey)
	}

	if cfg.Temporal == nil {
		cfg.Temporal = &TemporalConfig{}
	}
	if strings.TrimSpace(o.TemporalHost) != "" {
		cfg.Temporal.Host = strings.TrimSpace(o.TemporalHost)
	}
	if o.TemporalPort > 0 {
		cfg.Temporal.Port = o.TemporalPort
	}
	if strings.TrimSpace(o.TemporalNamespace) != "" {
		cfg.Temporal.Namespace = strings.TrimSpace(o.TemporalNamespace)
	}
	if strings.TrimSpace(o.TemporalTaskQueue) != "" {
		cfg.Temporal.TaskQueue = strings.TrimSpace(o.TemporalTaskQueue)
	}
	if o.LLMUsage {
		cfg.LLMUsage = true
	}
	// Re-apply built-in temporal defaults for any field still empty.
	ensureConfigStructs(cfg)
}
