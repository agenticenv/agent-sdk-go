package main

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/mcp"
)

func TestBuildMCPServers_allDisabled(t *testing.T) {
	cfg := &Config{
		MCP: &MCPRootConfig{
			Servers: []MCPServerYAML{
				{Enabled: ptrBool(false), Name: "a", Transport: "stdio", Command: "true"},
			},
		},
	}
	got, err := BuildMCPServers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no servers, got %#v", got)
	}
}

func TestBuildMCPServers_stdio(t *testing.T) {
	cfg := &Config{
		MCP: &MCPRootConfig{
			Servers: []MCPServerYAML{
				{Name: "local", Transport: "stdio", Command: "go", Args: []string{"version"}},
			},
		},
	}
	got, err := BuildMCPServers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	mc := got["local"]
	std, ok := mc.Transport.(mcp.MCPStdio)
	if !ok || std.Command != "go" {
		t.Fatalf("transport %#v", mc.Transport)
	}
}

func ptrBool(b bool) *bool { return &b }
