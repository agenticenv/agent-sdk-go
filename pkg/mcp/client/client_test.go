package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestNewClient_EmptyName(t *testing.T) {
	_, t2 := sdkmcp.NewInMemoryTransports()
	_, err := NewClient("   ", types.MCPLoopback{Transport: t2})
	if err == nil || !strings.Contains(err.Error(), "name is empty") {
		t.Fatalf("got %v", err)
	}
}

func TestNewClient_NilTransport(t *testing.T) {
	var tc types.MCPTransportConfig
	_, err := NewClient("x", tc)
	if err == nil || !strings.Contains(err.Error(), "transport config is nil") {
		t.Fatalf("got %v", err)
	}
}

func TestClient_Name_nilReceiver(t *testing.T) {
	var c *Client
	if c.Name() != "" {
		t.Fatal("nil Name should be empty")
	}
}

func TestClient_NilReceiver_PingListCall(t *testing.T) {
	var c *Client
	ctx := context.Background()
	if err := c.Ping(ctx); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("Ping: %v", err)
	}
	if _, err := c.ListTools(ctx); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("ListTools: %v", err)
	}
	if _, err := c.CallTool(ctx, "t", nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("CallTool: %v", err)
	}
}

func TestClient_Close_nilReceiver(t *testing.T) {
	var c *Client
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClient_CallTool_InvalidArgumentsJSON(t *testing.T) {
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
	defer func() { _ = c.Close() }()

	_, err = c.CallTool(ctx, "ping", json.RawMessage(`{`))
	if err == nil || !strings.Contains(err.Error(), "mcp tools/call arguments") {
		t.Fatalf("got %v", err)
	}
}

func TestClient_OperationAfterClose(t *testing.T) {
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
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	err = c.Ping(ctx)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Ping after Close: %v", err)
	}
}

func TestTransportFromConfig_nil(t *testing.T) {
	_, err := transportFromConfig(nil)
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("got %v", err)
	}
}

func TestMcpRetryDelay_capped(t *testing.T) {
	if d := mcpRetryDelay(1); d != 50*time.Millisecond {
		t.Fatalf("1: %v", d)
	}
	if d := mcpRetryDelay(1000); d != 2*time.Second {
		t.Fatalf("capped: %v", d)
	}
}

func TestWaitMCPRetry_parentCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitMCPRetry(ctx, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
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
