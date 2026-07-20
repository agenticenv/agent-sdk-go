package agent

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
	sdkagent "github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func TestRegisterConfiguredToolsRespectsEnabled(t *testing.T) {
	cfg := &config.Config{
		Tools: map[string]config.ToolConfig{
			config.ToolEcho:       {Enabled: true},
			config.ToolWeather:    {Enabled: false},
			config.ToolCalculator: {Enabled: true},
		},
	}

	reg := sdkagent.NewToolRegistry()
	if err := registerConfiguredTools(reg, cfg); err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, tool := range reg.List() {
		names[tool.Name()] = true
	}

	if !names[config.ToolEcho] {
		t.Fatal("expected echo registered")
	}
	if !names[config.ToolCalculator] {
		t.Fatal("expected calculator registered")
	}
	if names[config.ToolWeather] {
		t.Fatal("expected weather disabled")
	}
	// Omitted tools default enabled.
	if !names[config.ToolSearch] {
		t.Fatal("expected search registered by default")
	}
}

func TestRegisterConfiguredToolsAllDisabled(t *testing.T) {
	cfg := &config.Config{Tools: map[string]config.ToolConfig{}}
	for _, name := range config.BuiltinToolNames {
		cfg.Tools[name] = config.ToolConfig{Enabled: false}
	}
	reg := sdkagent.NewToolRegistry()
	if err := registerConfiguredTools(reg, cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(reg.List()); got != 0 {
		t.Fatalf("expected empty registry, got %d tools", got)
	}
}
