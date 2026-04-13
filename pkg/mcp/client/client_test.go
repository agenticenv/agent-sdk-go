package client

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTransportFromConfig_loopback(t *testing.T) {
	_, tr := sdkmcp.NewInMemoryTransports()
	got, err := transportFromConfig(types.MCPLoopback{Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	if got != tr {
		t.Fatal("expected same transport pointer")
	}
}

func TestNewClient_streamableHTTP_validateAtNewClient(t *testing.T) {
	_, err := NewClient("bad", types.MCPStreamableHTTP{})
	if err == nil {
		t.Fatal("expected error for invalid streamable_http config")
	}
}

func TestBuildConfig_invalidToolFilter(t *testing.T) {
	_, err := BuildConfig(WithToolFilter(mcp.MCPToolFilter{AllowTools: []string{"a"}, BlockTools: []string{"b"}}))
	if err == nil {
		t.Fatal("expected error")
	}
}

// In-memory MCP transports allow at most one client Connect per pipe; each test uses the pipe once.

func TestClient_ping_inMemory(t *testing.T) {
	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "srv", Version: "v0.0.1"}, nil)
	ss, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	c, err := NewClient("unit", types.MCPLoopback{Transport: t2})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClient_listTools_inMemory(t *testing.T) {
	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "srv", Version: "v0.0.1"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "ping", Description: "p", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	ss, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	c, err := NewClient("unit", types.MCPLoopback{Transport: t2})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "ping" {
		t.Fatalf("specs = %#v", specs)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClient_listTools_withToolFilter(t *testing.T) {
	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "srv", Version: "v0.0.1"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "ping", Description: "p", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "pong", Description: "q", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	ss, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	c, err := NewClient("unit", types.MCPLoopback{Transport: t2},
		WithToolFilter(mcp.MCPToolFilter{AllowTools: []string{"ping"}}))
	if err != nil {
		t.Fatal(err)
	}
	specs, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "ping" {
		t.Fatalf("specs = %#v", specs)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClient_callTool_inMemory(t *testing.T) {
	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "srv", Version: "v0.0.1"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "ping", Description: "p", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	ss, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	c, err := NewClient("unit", types.MCPLoopback{Transport: t2})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.CallTool(ctx, "ping", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("empty call result")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
