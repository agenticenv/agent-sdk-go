package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	testutil "github.com/agenticenv/agent-sdk-go/internal/testing"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	"gopkg.in/yaml.v3"
)

const BenchmarkTreeSeed int64 = 42
const defaultMemoryUserID = "benchmark-user"

type Config struct {
	Runtime  string         `yaml:"runtime"`
	Temporal TemporalConfig `yaml:"temporal"`
	LLM      LLMConfig      `yaml:"llm"`
	Tool     ToolConfig     `yaml:"tool"`
	Agent    AgentConfig    `yaml:"agent"`
	Memory   MemoryConfig   `yaml:"memory"`
	Logger   LoggerConfig   `yaml:"logger"`
	Output   OutputConfig   `yaml:"output"`
}

type TemporalConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	Namespace    string `yaml:"namespace"`
	TaskQueue    string `yaml:"task_queue"`
	WorkersCount int    `yaml:"workers_count"`
}

type LLMConfig struct {
	LatencyMs  int `yaml:"latency_ms"`
	JitterMs   int `yaml:"jitter_ms"`
	MockTokens int `yaml:"mock_tokens"`
}

type ToolConfig struct {
	LatencyMs int `yaml:"latency_ms"`
	JitterMs  int `yaml:"jitter_ms"`
}

type AgentConfig struct {
	Runs            int              `yaml:"runs"`
	Concurrent      bool             `yaml:"concurrent"`
	ConcurrentCount int              `yaml:"concurrent_count"`
	Tools           AgentToolsConfig `yaml:"tools"`
	Subagents       SubagentsConfig  `yaml:"subagents"`
}

type AgentToolsConfig struct {
	Count     int    `yaml:"count"`
	Execution string `yaml:"execution"`
}

type SubagentsConfig struct {
	Count  int `yaml:"count"`
	Levels int `yaml:"levels"`
}

// MemoryConfig configures long-term memory for benchmark runs.
type MemoryConfig struct {
	Enabled   bool   `yaml:"enabled"`
	StoreMode string `yaml:"store_mode"`
	UserID    string `yaml:"user_id"`
}

type LoggerConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
	Level   string `yaml:"level"`
}

type OutputConfig struct {
	Console bool   `yaml:"console"`
	File    bool   `yaml:"file"`
	Dir     string `yaml:"dir"`
	Format  string `yaml:"format"`
}

func (c *Config) UseTemporal() bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(c.Runtime), "temporal")
}

func (c *Config) ExternalWorkersEnabled() bool {
	return c.UseTemporal() && c.Temporal.WorkersCount > 0
}

// MemoryEnabled reports whether long-term memory is wired for benchmark runs.
func (c *Config) MemoryEnabled() bool {
	return c != nil && c.Memory.Enabled
}

func (m *MemoryConfig) applyDefaults() {
	if m == nil {
		return
	}
	if strings.TrimSpace(m.UserID) == "" {
		m.UserID = defaultMemoryUserID
	}
	if strings.TrimSpace(m.StoreMode) == "" {
		m.StoreMode = string(memory.StoreModeOnDemand)
	}
}

func parseMemoryStoreMode(raw string) (memory.StoreMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(memory.StoreModeOnDemand), "on-demand", "on_demand":
		return memory.StoreModeOnDemand, nil
	case string(memory.StoreModeAlways):
		return memory.StoreModeAlways, nil
	default:
		return "", fmt.Errorf("memory.store_mode must be %q or %q", memory.StoreModeOnDemand, memory.StoreModeAlways)
	}
}

// MemoryAgentOption returns WithMemory when memory is enabled.
func MemoryAgentOption(cfg *Config) (agent.Option, error) {
	if cfg == nil || !cfg.MemoryEnabled() {
		return nil, nil
	}
	cfg.Memory.applyDefaults()
	mode, err := parseMemoryStoreMode(cfg.Memory.StoreMode)
	if err != nil {
		return nil, err
	}
	memCfg := memory.DefaultConfig(testutil.NewInmemMemory())
	memCfg.Store.Mode = mode
	memCfg.Recall.Enabled = true
	return agent.WithMemory(memCfg), nil
}

func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Agent.Runs <= 0 {
		return fmt.Errorf("agent.runs must be > 0")
	}
	if c.Agent.Concurrent && c.Agent.ConcurrentCount <= 0 {
		return fmt.Errorf("agent.concurrent_count must be > 0 when concurrent is true")
	}
	if c.Agent.Tools.Count <= 0 && !c.Memory.Enabled {
		return fmt.Errorf("agent.tools.count must be > 0 when memory is disabled")
	}
	if c.Agent.Subagents.Levels < 0 {
		return fmt.Errorf("agent.subagents.levels must be >= 0")
	}
	if c.Agent.Subagents.Levels > 5 {
		return fmt.Errorf("agent.subagents.levels must be <= 5")
	}
	if c.Agent.Subagents.Count < 0 {
		return fmt.Errorf("agent.subagents.count must be >= 0")
	}
	if c.Temporal.WorkersCount < 0 {
		return fmt.Errorf("temporal.workers_count must be >= 0")
	}
	if c.LLM.MockTokens <= 0 {
		c.LLM.MockTokens = 500
	}
	if c.Logger.Dir == "" {
		c.Logger.Dir = "benchmarks/logs"
	}
	if strings.TrimSpace(c.Logger.Level) == "" {
		c.Logger.Level = "info"
	}
	if c.Output.Dir == "" {
		c.Output.Dir = "benchmarks/reports"
	}
	if c.Output.Format == "" {
		c.Output.Format = "json"
	}
	if c.Temporal.TaskQueue == "" {
		c.Temporal.TaskQueue = "agent-sdk-go"
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
	c.Memory.applyDefaults()
	if c.Memory.Enabled {
		if _, err := parseMemoryStoreMode(c.Memory.StoreMode); err != nil {
			return err
		}
	}
	return nil
}

func defaultConfigPath() string {
	for _, candidate := range []string{"benchmarks/config.yaml", "config.yaml"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "benchmarks/config.yaml"
}

// DefaultConfigPath returns the default benchmark config file path.
func DefaultConfigPath() string { return defaultConfigPath() }

func FindRepoRoot(from string) (string, error) {
	dir, err := filepath.Abs(from)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", from)
		}
		dir = parent
	}
}

func (c *Config) OutputDir(repoRoot string) string {
	return resolveRepoPath(repoRoot, c.Output.Dir)
}

func (c *Config) LogDir(repoRoot string) string {
	return resolveRepoPath(repoRoot, c.Logger.Dir)
}

func resolveRepoPath(repoRoot, dir string) string {
	dir = strings.TrimSpace(dir)
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(repoRoot, dir)
}
