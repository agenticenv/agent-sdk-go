package agent

import (
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestMcpConfigFingerprint_empty(t *testing.T) {
	if got := mcpConfigFingerprint(nil, nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := mcpConfigFingerprint(MCPServers{}, nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestMcpConfigFingerprint_differentURL(t *testing.T) {
	a := MCPServers{
		"srv": MCPConfig{Transport: types.MCPStreamableHTTP{URL: "https://a.example/mcp"}},
	}
	b := MCPServers{
		"srv": MCPConfig{Transport: types.MCPStreamableHTTP{URL: "https://b.example/mcp"}},
	}
	gotA, gotB := mcpConfigFingerprint(a, nil), mcpConfigFingerprint(b, nil)
	if gotA == gotB || gotA == "" || gotB == "" {
		t.Fatalf("expected two distinct non-empty fingerprints: %q vs %q", gotA, gotB)
	}
}

func TestMcpConfigFingerprint_timeoutChange(t *testing.T) {
	a := MCPServers{
		"srv": MCPConfig{
			Transport: types.MCPStreamableHTTP{URL: "https://x.example/mcp"},
			Timeout:   10 * time.Second,
		},
	}
	b := MCPServers{
		"srv": MCPConfig{
			Transport: types.MCPStreamableHTTP{URL: "https://x.example/mcp"},
			Timeout:   20 * time.Second,
		},
	}
	if mcpConfigFingerprint(a, nil) == mcpConfigFingerprint(b, nil) {
		t.Fatal("expected different fingerprints when Timeout differs")
	}
}

func TestMcpConfigFingerprint_extraClientNames(t *testing.T) {
	gotA := mcpConfigFingerprint(nil, []string{"alpha", "beta"})
	gotB := mcpConfigFingerprint(nil, []string{"gamma"})
	if gotA == gotB || gotA == "" {
		t.Fatalf("expected distinct non-empty fingerprints: %q vs %q", gotA, gotB)
	}
}

func TestMcpToolName(t *testing.T) {
	if got := mcpToolName("  remote  ", "  search  "); got != "mcp_remote_search" {
		t.Fatalf("got %q", got)
	}
	if mcpToolName("", "x") != "" || mcpToolName("x", "") != "" || mcpToolName(" ", "y") != "" {
		t.Fatal("expected empty for missing server or tool id")
	}
	tool := NewMCPTool("  srv  ", interfaces.ToolSpec{Name: "  tid  ", Description: "d"}, nil)
	if tool.Name() != mcpToolName("srv", "tid") || tool.Name() != "mcp_srv_tid" {
		t.Fatalf("MCPTool.Name = %q want mcp_srv_tid", tool.Name())
	}
}
