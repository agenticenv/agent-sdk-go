package setup

import (
	"fmt"
	"os"
	"strings"

	testutil "github.com/agenticenv/agent-sdk-go/internal/testing"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	"gopkg.in/yaml.v3"
)

const (
	DefaultAgentName          = "eval-agent"
	DefaultToolCount          = 3
	DefaultMockTokens         = 500
	DefaultSystemPrompt       = "You are an evaluation agent. Use available tools when helpful, then answer concisely."
	DefaultRuntime            = RuntimeLocal
	DefaultMemoryUserID       = "eval-user"
	DefaultMemoryStoreMode    = memory.StoreModeOnDemand
	MemoryScenarioStoreRecall = "store_recall"
)

// Runtime selects the agent execution backend.
type Runtime string

const (
	RuntimeLocal    Runtime = "local"
	RuntimeTemporal Runtime = "temporal"
	RuntimeRestate  Runtime = "restate"
)

// LLMConfig configures the built-in mock LLM (internal defaults, not in YAML).
type LLMConfig struct {
	MockTokens int
}

// ToolConfig configures mock tools (internal defaults, not in YAML).
type ToolConfig struct{}

// TemporalConfig configures Temporal when Runtime is temporal.
type TemporalConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	Namespace string `yaml:"namespace"`
	TaskQueue string `yaml:"task_queue"`
}

// RestateConfig configures Restate when Runtime is restate.
type RestateConfig struct {
	IngressURL            string `yaml:"ingress_url"`
	AdminURL              string `yaml:"admin_url"`
	AuthKey               string `yaml:"auth_key"`
	EndpointListenAddress string `yaml:"endpoint_listen_address"`
	DeploymentURL         string `yaml:"deployment_url"`
}

// MemoryConfig configures long-term memory for eval harness runs.
type MemoryConfig struct {
	Enabled      bool
	StoreMode    memory.StoreMode
	UserID       string
	Scenario     string
	StorePrompt  string
	RecallPrompt string
}

// Config holds settings for a single eval agent run.
type Config struct {
	UserPrompt   string
	Runtime      Runtime
	Temporal     TemporalConfig
	Restate      RestateConfig
	AgentName    string
	SystemPrompt string
	LLM          LLMConfig
	Tool         ToolConfig
	ToolCount    int
	Memory       MemoryConfig
	LLMClient    interfaces.LLMClient
	ToolRegistry agent.ToolRegistry
	Logger       logger.Logger
}

// FileConfig is the YAML configuration for eval-harness runs.
type FileConfig struct {
	Runtime    string           `yaml:"runtime"`
	UserPrompt string           `yaml:"user_prompt"`
	Agent      FileAgentConfig  `yaml:"agent"`
	Memory     FileMemoryConfig `yaml:"memory"`
	Temporal   TemporalConfig   `yaml:"temporal"`
	Restate    RestateConfig    `yaml:"restate"`
}

// FileMemoryConfig holds memory fields from YAML.
type FileMemoryConfig struct {
	Enabled      bool   `yaml:"enabled"`
	StoreMode    string `yaml:"store_mode"`
	UserID       string `yaml:"user_id"`
	Scenario     string `yaml:"scenario"`
	StorePrompt  string `yaml:"store_prompt"`
	RecallPrompt string `yaml:"recall_prompt"`
}

// FileAgentConfig holds agent fields from YAML.
type FileAgentConfig struct {
	Name         string `yaml:"name"`
	SystemPrompt string `yaml:"system_prompt"`
	ToolCount    int    `yaml:"tool_count"`
}

// Config returns a runner Config from the file config.
func (f *FileConfig) Config() Config {
	if f == nil {
		return Config{}
	}
	storeMode, _ := ParseMemoryStoreMode(f.Memory.StoreMode)
	return Config{
		UserPrompt:   f.UserPrompt,
		Runtime:      Runtime(f.Runtime),
		Temporal:     f.Temporal,
		Restate:      f.Restate,
		AgentName:    f.Agent.Name,
		SystemPrompt: f.Agent.SystemPrompt,
		ToolCount:    f.Agent.ToolCount,
		Memory: MemoryConfig{
			Enabled:      f.Memory.Enabled,
			StoreMode:    storeMode,
			UserID:       f.Memory.UserID,
			Scenario:     f.Memory.Scenario,
			StorePrompt:  f.Memory.StorePrompt,
			RecallPrompt: f.Memory.RecallPrompt,
		},
	}
}

// LoadConfig reads and validates eval-harness config from a YAML file.
func LoadConfig(path string) (*FileConfig, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg FileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// DefaultConfigPath returns the default eval-harness config file path.
func DefaultConfigPath() string { return defaultConfigPath() }

func defaultConfigPath() string {
	for _, candidate := range []string{
		"eval-harness/runner/config.yaml",
		"runner/config.yaml",
		"config.yaml",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "config.yaml"
}

func (f *FileConfig) validate() error {
	if f == nil {
		return fmt.Errorf("config is required")
	}
	if strings.TrimSpace(f.UserPrompt) == "" && !strings.EqualFold(strings.TrimSpace(f.Memory.Scenario), MemoryScenarioStoreRecall) {
		return fmt.Errorf("user_prompt is required")
	}
	switch strings.ToLower(strings.TrimSpace(f.Runtime)) {
	case "", string(RuntimeLocal):
		if f.Runtime == "" {
			f.Runtime = string(RuntimeLocal)
		}
	case string(RuntimeTemporal), string(RuntimeRestate):
	default:
		return fmt.Errorf("runtime must be %q, %q, or %q", RuntimeLocal, RuntimeTemporal, RuntimeRestate)
	}
	if f.Agent.ToolCount <= 0 && !f.Memory.Enabled {
		f.Agent.ToolCount = DefaultToolCount
	}
	if f.Agent.Name == "" {
		f.Agent.Name = DefaultAgentName
	}
	if f.Agent.SystemPrompt == "" {
		f.Agent.SystemPrompt = DefaultSystemPrompt
	}
	if f.Temporal.TaskQueue == "" {
		f.Temporal.TaskQueue = "eval-harness"
	}
	if f.Temporal.Port == 0 {
		f.Temporal.Port = 7233
	}
	if f.Temporal.Host == "" {
		f.Temporal.Host = "localhost"
	}
	if f.Temporal.Namespace == "" {
		f.Temporal.Namespace = "default"
	}
	if f.Restate.IngressURL == "" {
		f.Restate.IngressURL = "http://localhost:8080"
	}
	if f.Restate.AdminURL == "" {
		f.Restate.AdminURL = "http://localhost:9070"
	}
	if f.Restate.EndpointListenAddress == "" {
		f.Restate.EndpointListenAddress = ":9080"
	}
	if f.Memory.Enabled {
		if _, err := ParseMemoryStoreMode(f.Memory.StoreMode); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(f.Memory.Scenario), MemoryScenarioStoreRecall) {
			if strings.TrimSpace(f.Memory.StorePrompt) == "" {
				return fmt.Errorf("memory.store_prompt is required when memory.scenario is %q", MemoryScenarioStoreRecall)
			}
			if strings.TrimSpace(f.Memory.RecallPrompt) == "" {
				return fmt.Errorf("memory.recall_prompt is required when memory.scenario is %q", MemoryScenarioStoreRecall)
			}
		}
	}
	return nil
}

// UseTemporal reports whether cfg selects the Temporal runtime.
func (c *Config) UseTemporal() bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(string(c.Runtime)), string(RuntimeTemporal))
}

// UseRestate reports whether cfg selects the Restate runtime.
func (c *Config) UseRestate() bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(string(c.Runtime)), string(RuntimeRestate))
}

// UseDurableRuntime reports whether cfg selects Temporal or Restate.
func (c *Config) UseDurableRuntime() bool {
	return c.UseTemporal() || c.UseRestate()
}

// MemoryEnabled reports whether memory is wired for this run.
func (c *Config) MemoryEnabled() bool {
	return c != nil && c.Memory.Enabled
}

// UsesMemoryScenario reports whether the runner executes a multi-step memory scenario.
func (c *Config) UsesMemoryScenario() bool {
	return c.MemoryEnabled() && strings.EqualFold(strings.TrimSpace(c.Memory.Scenario), MemoryScenarioStoreRecall)
}

// ApplyMemoryDefaults fills unset memory config fields.
func (m *MemoryConfig) ApplyMemoryDefaults() {
	if m == nil {
		return
	}
	if strings.TrimSpace(m.UserID) == "" {
		m.UserID = DefaultMemoryUserID
	}
	if strings.TrimSpace(string(m.StoreMode)) == "" {
		m.StoreMode = DefaultMemoryStoreMode
	}
}

// ApplyDefaults fills unset config fields.
func (c *Config) ApplyDefaults() {
	if c == nil {
		return
	}
	if strings.TrimSpace(string(c.Runtime)) == "" {
		c.Runtime = DefaultRuntime
	}
	if c.AgentName == "" {
		c.AgentName = DefaultAgentName
	}
	if c.SystemPrompt == "" {
		c.SystemPrompt = DefaultSystemPrompt
	}
	if c.ToolCount <= 0 && !c.MemoryEnabled() {
		c.ToolCount = DefaultToolCount
	}
	if c.LLM.MockTokens <= 0 {
		c.LLM.MockTokens = DefaultMockTokens
	}
	if c.Logger == nil {
		c.Logger = logger.NoopLogger()
	}
	c.Memory.ApplyMemoryDefaults()
	if c.Temporal.TaskQueue == "" {
		c.Temporal.TaskQueue = "eval-harness"
	}
	if c.Temporal.Port == 0 {
		c.Temporal.Port = 7233
	}
	if c.Temporal.Host == "" {
		c.Temporal.Host = "localhost"
	}
	if c.Temporal.Namespace == "" {
		c.Temporal.Namespace = "default"
	}
	if c.Restate.IngressURL == "" {
		c.Restate.IngressURL = "http://localhost:8080"
	}
	if c.Restate.AdminURL == "" {
		c.Restate.AdminURL = "http://localhost:9070"
	}
	if c.Restate.EndpointListenAddress == "" {
		c.Restate.EndpointListenAddress = ":9080"
	}
}

// ValidateMemory checks memory-related config when enabled.
func (c *Config) ValidateMemory() error {
	if c == nil || !c.Memory.Enabled {
		return nil
	}
	c.Memory.ApplyMemoryDefaults()
	switch c.Memory.StoreMode {
	case memory.StoreModeOnDemand, memory.StoreModeAlways:
	default:
		return fmt.Errorf("memory.store_mode must be %q or %q", memory.StoreModeOnDemand, memory.StoreModeAlways)
	}
	if !c.UsesMemoryScenario() {
		return nil
	}
	if strings.TrimSpace(c.Memory.StorePrompt) == "" {
		return fmt.Errorf("memory.store_prompt is required when memory.scenario is %q", MemoryScenarioStoreRecall)
	}
	if strings.TrimSpace(c.Memory.RecallPrompt) == "" {
		return fmt.Errorf("memory.recall_prompt is required when memory.scenario is %q", MemoryScenarioStoreRecall)
	}
	return nil
}

// Validate checks required config fields.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	switch strings.ToLower(strings.TrimSpace(string(c.Runtime))) {
	case string(RuntimeLocal), string(RuntimeTemporal), string(RuntimeRestate):
	default:
		return fmt.Errorf("runtime must be %q, %q, or %q", RuntimeLocal, RuntimeTemporal, RuntimeRestate)
	}
	if !c.UsesMemoryScenario() && c.UserPrompt == "" {
		return fmt.Errorf("user prompt is required")
	}
	return c.ValidateMemory()
}

// ParseMemoryStoreMode parses eval harness store mode strings.
func ParseMemoryStoreMode(raw string) (memory.StoreMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(memory.StoreModeOnDemand), "on-demand", "on_demand":
		return memory.StoreModeOnDemand, nil
	case string(memory.StoreModeAlways):
		return memory.StoreModeAlways, nil
	default:
		return "", fmt.Errorf("memory store mode must be %q or %q", memory.StoreModeOnDemand, memory.StoreModeAlways)
	}
}

// MemoryAgentOption returns WithMemory when memory is enabled.
func MemoryAgentOption(cfg Config) (agent.Option, error) {
	if !cfg.MemoryEnabled() {
		return nil, nil
	}
	cfg.Memory.ApplyMemoryDefaults()
	memCfg := memory.DefaultConfig(testutil.NewInmemMemory())
	memCfg.Store.Mode = cfg.Memory.StoreMode
	memCfg.Recall.Enabled = true
	return agent.WithMemory(memCfg), nil
}
