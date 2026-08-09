package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestForShowEffectiveMerged(t *testing.T) {
	enabled := true
	disabled := false
	cfg := &Config{
		Runtime: "local",
		Temporal: &TemporalConfig{
			Host:      "localhost",
			Port:      7233,
			Namespace: "default",
			TaskQueue: "agent-sdk-go",
		},
		LLM: &LLMConfig{
			Provider: "openai",
			APIKey:   "sk-secret",
			Model:    "gpt-4o",
		},
		Logger: &LoggerConfig{Level: "info"},
		Tools: map[string]ToolConfig{
			ToolEcho:    {Enabled: true},
			ToolWeather: {Enabled: false},
		},
		MCP: &MCPRootConfig{
			Servers: []MCPServerYAML{
				{Enabled: &enabled, Name: "fs", Transport: "stdio", Command: "npx"},
				{Enabled: &disabled, Name: "remote", Transport: "streamable_http", URL: "https://example.com"},
			},
		},
	}

	out := ForShow(cfg)
	if out.Temporal != nil {
		t.Fatal("temporal should be omitted for local runtime")
	}
	if out.LLM == nil || out.LLM.APIKey != "***" {
		t.Fatalf("api key should be redacted, got %+v", out.LLM)
	}
	if !out.Tools[ToolEcho].Enabled || out.Tools[ToolWeather].Enabled {
		t.Fatalf("tools should keep merged enabled flags; got %#v", out.Tools)
	}
	if out.MCP == nil || len(out.MCP.Servers) != 2 {
		t.Fatalf("mcp should include all merged servers, got %#v", out.MCP)
	}

	b, err := yaml.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "temporal:") {
		t.Fatalf("yaml should omit temporal for local:\n%s", s)
	}
	if !strings.Contains(s, "weather:") || !strings.Contains(s, "remote") {
		t.Fatalf("yaml should include merged tools/mcp state:\n%s", s)
	}
}

func TestForShowIncludesTemporalWhenSelected(t *testing.T) {
	cfg := &Config{
		Runtime:  "temporal",
		Temporal: &TemporalConfig{Host: "localhost", Port: 7233},
		LLM:      &LLMConfig{Provider: "openai", Model: "gpt-4o"},
	}
	out := ForShow(cfg)
	if out.Temporal == nil || out.Temporal.Host != "localhost" {
		t.Fatalf("expected temporal in show, got %#v", out.Temporal)
	}
	if out.Restate != nil {
		t.Fatalf("restate should be omitted for temporal runtime, got %#v", out.Restate)
	}
}

func TestForShowIncludesRestateWhenSelected(t *testing.T) {
	cfg := &Config{
		Runtime: "restate",
		Restate: &RestateConfig{
			IngressURL:            "http://localhost:8080",
			AdminURL:              "http://localhost:9070",
			AuthKey:               "secret-key",
			EndpointListenAddress: ":9080",
		},
		LLM: &LLMConfig{Provider: "openai", Model: "gpt-4o"},
	}
	out := ForShow(cfg)
	if out.Restate == nil || out.Restate.IngressURL != "http://localhost:8080" {
		t.Fatalf("expected restate in show, got %#v", out.Restate)
	}
	if out.Restate.AuthKey != "***" {
		t.Fatalf("auth key should be redacted, got %q", out.Restate.AuthKey)
	}
	if out.Temporal != nil {
		t.Fatalf("temporal should be omitted for restate runtime, got %#v", out.Temporal)
	}
}
