package agent

import (
	"context"
	"encoding/json"
	"strings"
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

type mcpExecuteStub struct{}

func (mcpExecuteStub) Name() string { return "stub" }
func (mcpExecuteStub) Ping(ctx context.Context) error {
	return nil
}
func (mcpExecuteStub) ListTools(ctx context.Context) ([]interfaces.ToolSpec, error) {
	return nil, nil
}
func (mcpExecuteStub) CallTool(ctx context.Context, tool string, input json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}
func (mcpExecuteStub) Close() error { return nil }

type mcpNonJSONStub struct{}

func (mcpNonJSONStub) Name() string { return "raw" }
func (mcpNonJSONStub) Ping(ctx context.Context) error {
	return nil
}
func (mcpNonJSONStub) ListTools(ctx context.Context) ([]interfaces.ToolSpec, error) {
	return nil, nil
}
func (mcpNonJSONStub) CallTool(ctx context.Context, tool string, input json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`not-valid-json{`), nil
}
func (mcpNonJSONStub) Close() error { return nil }

func TestMCPTool_Execute_JSON(t *testing.T) {
	tool := NewMCPTool("srv", interfaces.ToolSpec{Name: "echo", Description: "d", Parameters: map[string]any{"type": "object"}}, mcpExecuteStub{})
	out, err := tool.Execute(context.Background(), map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.(map[string]any)
	if !ok || m["ok"] != true {
		t.Fatalf("got %#v", out)
	}
}

func TestMCPTool_Execute_nonJSONResult(t *testing.T) {
	tool := NewMCPTool("srv", interfaces.ToolSpec{Name: "raw", Description: "d"}, mcpNonJSONStub{})
	out, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := out.(string)
	if !ok || !strings.Contains(s, "not-valid") {
		t.Fatalf("got %#v", out)
	}
}

func TestMCPTool_Execute_emptyPayload(t *testing.T) {
	tool := NewMCPTool("srv", interfaces.ToolSpec{Name: "e", Description: "d"}, &mcpEmptyResultStub{})
	out, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("want empty string, got %#v", out)
	}
}

type mcpEmptyResultStub struct{}

func (mcpEmptyResultStub) Name() string { return "e" }
func (mcpEmptyResultStub) Ping(ctx context.Context) error {
	return nil
}
func (mcpEmptyResultStub) ListTools(ctx context.Context) ([]interfaces.ToolSpec, error) {
	return nil, nil
}
func (mcpEmptyResultStub) CallTool(ctx context.Context, tool string, input json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
func (mcpEmptyResultStub) Close() error { return nil }

func TestMCPTool_Execute_nilClient(t *testing.T) {
	tool := &MCPTool{ServerName: "s", Spec: interfaces.ToolSpec{Name: "t"}, Client: nil}
	_, err := tool.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "nil client") {
		t.Fatalf("got %v", err)
	}
}
