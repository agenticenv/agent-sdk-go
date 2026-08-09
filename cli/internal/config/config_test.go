package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
)

func TestDefaultConfigDirUsesXDG(t *testing.T) {
	dir := t.TempDir()
	cfgHome := filepath.Join(dir, "config")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	got := DefaultConfigDir()
	want := filepath.Join(cfgHome, "agctl")
	if got != want {
		t.Fatalf("DefaultConfigDir = %q, want %q", got, want)
	}
}

func TestLoadConfigLayers(t *testing.T) {
	dir := t.TempDir()
	cfgHome := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("AGCTL_CONFIG", "")
	t.Setenv("AGCTL_LLM_APIKEY", "")
	t.Setenv("AGCTL_LLM_MODEL", "")
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	embedded := []byte(`
runtime: local
llm:
  provider: openai
  model: from-embed
temporal:
  host: localhost
  port: 7233
  namespace: default
  taskQueue: agent-sdk-go
logger:
  level: error
  output: logs/agctl.log
  format: json
mcp:
  servers: []
`)

	// Layer 1 only: embedded
	cfg, err := LoadConfig("", embedded)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "from-embed" {
		t.Fatalf("embed model: %q", cfg.LLM.Model)
	}

	// Layer 2: XDG overlays
	xdgPath := filepath.Join(cfgHome, "agctl", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgPath, []byte("llm:\n  model: from-xdg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig("", embedded)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "from-xdg" || cfg.LLM.Provider != "openai" {
		t.Fatalf("xdg merge: model=%q provider=%q", cfg.LLM.Model, cfg.LLM.Provider)
	}

	// Layer 3: explicit partial override
	explicit := filepath.Join(dir, "partial.yaml")
	if err := os.WriteFile(explicit, []byte("llm:\n  model: from-explicit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(explicit, embedded)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "from-explicit" || cfg.LLM.Provider != "openai" {
		t.Fatalf("explicit merge: model=%q provider=%q", cfg.LLM.Model, cfg.LLM.Provider)
	}

	// Layer 4: env wins over files
	t.Setenv("AGCTL_LLM_MODEL", "from-env")
	cfg, err = LoadConfig(explicit, embedded)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "from-env" {
		t.Fatalf("env: model=%q", cfg.LLM.Model)
	}
}

func TestLoadConfigMissingExplicitErrors(t *testing.T) {
	t.Setenv("AGCTL_CONFIG", "")
	embedded := []byte("runtime: local\nllm:\n  provider: openai\n  model: gpt-4o\n")
	_, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"), embedded)
	if err == nil {
		t.Fatal("expected error for missing explicit config")
	}
}

func TestLoadConfigResolvesAPIKey(t *testing.T) {
	t.Setenv("AGCTL_CONFIG", "")
	t.Setenv("AGCTL_LLM_APIKEY", "from-env")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	embedded := []byte("runtime: local\nllm:\n  provider: openai\n  model: gpt-4o\n  apiKey: from-file\n")
	cfg, err := LoadConfig("", embedded)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "from-env" {
		t.Fatalf("expected env key, got %q", cfg.LLM.APIKey)
	}
}

func TestApplyAgentOverridesAPIKeyAndTemporal(t *testing.T) {
	cfg := &Config{
		Runtime: "local",
		LLM:     &LLMConfig{Provider: "openai", Model: "gpt-4o", APIKey: "from-file"},
	}
	ensureConfigStructs(cfg)

	ApplyAgentOverrides(cfg, AgentOverrides{
		Runtime:           "temporal",
		APIKey:            "from-flag",
		TemporalHost:      "temporal.example",
		TemporalPort:      7234,
		TemporalNamespace: "prod",
		TemporalTaskQueue: "my-queue",
	})
	if cfg.Runtime != "temporal" || cfg.LLM.APIKey != "from-flag" {
		t.Fatalf("runtime/key: runtime=%q key=%q", cfg.Runtime, cfg.LLM.APIKey)
	}
	if cfg.Temporal.Host != "temporal.example" || cfg.Temporal.Port != 7234 ||
		cfg.Temporal.Namespace != "prod" || cfg.Temporal.TaskQueue != "my-queue" {
		t.Fatalf("temporal overrides: %+v", cfg.Temporal)
	}

	// Runtime temporal with no temporal flags keeps built-in defaults.
	cfg2 := &Config{Runtime: "local", LLM: &LLMConfig{}}
	ensureConfigStructs(cfg2)
	ApplyAgentOverrides(cfg2, AgentOverrides{Runtime: "temporal"})
	if cfg2.Temporal.Host != "localhost" || cfg2.Temporal.Port != 7233 ||
		cfg2.Temporal.Namespace != "default" || cfg2.Temporal.TaskQueue != "agent-sdk-go" {
		t.Fatalf("expected temporal defaults, got %+v", cfg2.Temporal)
	}
}

func TestApplyAgentOverridesRestate(t *testing.T) {
	cfg := &Config{Runtime: "local", LLM: &LLMConfig{}}
	ensureConfigStructs(cfg)

	ApplyAgentOverrides(cfg, AgentOverrides{
		Runtime:                      "restate",
		RestateIngressURL:            "http://restate.example:8080",
		RestateAdminURL:              "http://restate.example:9070",
		RestateAuthKey:               "key",
		RestateEndpointListenAddress: ":9090",
		RestateDeploymentURL:         "http://host.docker.internal:9090",
	})
	if cfg.Runtime != "restate" {
		t.Fatalf("runtime: %q", cfg.Runtime)
	}
	if cfg.Restate.IngressURL != "http://restate.example:8080" ||
		cfg.Restate.AdminURL != "http://restate.example:9070" ||
		cfg.Restate.AuthKey != "key" ||
		cfg.Restate.EndpointListenAddress != ":9090" ||
		cfg.Restate.DeploymentURL != "http://host.docker.internal:9090" {
		t.Fatalf("restate overrides: %+v", cfg.Restate)
	}

	cfg2 := &Config{Runtime: "local", LLM: &LLMConfig{}}
	ensureConfigStructs(cfg2)
	ApplyAgentOverrides(cfg2, AgentOverrides{Runtime: "restate"})
	if cfg2.Restate.IngressURL != "http://localhost:8080" ||
		cfg2.Restate.AdminURL != "http://localhost:9070" ||
		cfg2.Restate.EndpointListenAddress != ":9080" {
		t.Fatalf("expected restate defaults, got %+v", cfg2.Restate)
	}
}

func TestToolEnabledDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgHome := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("AGCTL_CONFIG", "")
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	embedded := []byte(`
llm:
  provider: openai
  model: gpt-4o
tools:
  echo:
    enabled: true
  weather:
    enabled: true
`)
	xdgPath := filepath.Join(cfgHome, "agctl", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgPath, []byte("tools:\n  weather:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig("", embedded)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ToolEnabled(ToolEcho) {
		t.Fatal("echo should stay enabled")
	}
	if cfg.ToolEnabled(ToolWeather) {
		t.Fatal("weather should be disabled by XDG overlay")
	}
	if !cfg.ToolEnabled(ToolSearch) {
		t.Fatal("omitted built-in search should default enabled")
	}
}

func TestResolveConfigPath(t *testing.T) {
	t.Setenv("AGCTL_CONFIG", "")
	dir := t.TempDir()
	cfgHome := filepath.Join(dir, "config")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	explicit := filepath.Join(dir, "explicit.yaml")
	if got := ResolveConfigPath(explicit); got != explicit {
		t.Fatalf("explicit: got %q", got)
	}

	envPath := filepath.Join(dir, "env.yaml")
	t.Setenv("AGCTL_CONFIG", envPath)
	if got := ResolveConfigPath(""); got != envPath {
		t.Fatalf("env: got %q want %q", got, envPath)
	}
}
