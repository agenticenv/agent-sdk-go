package client

import (
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/mcp"
)

func TestBuildConfig_Defaults(t *testing.T) {
	c, err := BuildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "error" {
		t.Errorf("LogLevel = %q, want error", c.LogLevel)
	}
	if c.Logger == nil {
		t.Fatal("expected default Logger")
	}
	if c.Timeout != types.DefaultMCPTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, types.DefaultMCPTimeout)
	}
	if c.RetryAttempts != types.DefaultMCPRetryAttempts {
		t.Errorf("RetryAttempts = %d, want %d", c.RetryAttempts, types.DefaultMCPRetryAttempts)
	}
}

func TestBuildConfig_WithOptions(t *testing.T) {
	log := logger.NoopLogger()
	c, err := BuildConfig(
		WithLogger(log),
		WithLogLevel("debug"),
		WithTimeout(7*time.Second),
		WithRetryAttempts(5),
		WithToolFilter(mcp.MCPToolFilter{AllowTools: []string{"only-this"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if c.Logger != log {
		t.Error("Logger not set from WithLogger")
	}
	if c.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", c.LogLevel)
	}
	if c.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.RetryAttempts != 5 {
		t.Errorf("RetryAttempts = %d", c.RetryAttempts)
	}
	if len(c.ToolFilter.AllowTools) != 1 || c.ToolFilter.AllowTools[0] != "only-this" {
		t.Fatalf("ToolFilter = %+v", c.ToolFilter)
	}
}

func TestBuildConfig_NilOptionSkipped(t *testing.T) {
	var nilOpt Option
	c, err := BuildConfig(nilOpt, WithLogLevel("warn"))
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "warn" {
		t.Fatalf("LogLevel = %q", c.LogLevel)
	}
}

func TestBuildConfig_InvalidToolFilter(t *testing.T) {
	_, err := BuildConfig(WithToolFilter(mcp.MCPToolFilter{
		AllowTools: []string{"a"},
		BlockTools: []string{"b"},
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "mcp client config") {
		t.Errorf("got %v", err)
	}
}

func TestBuildConfig_ZeroTimeoutAndRetryUseDefaults(t *testing.T) {
	c, err := BuildConfig(
		WithTimeout(0),
		WithRetryAttempts(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != types.DefaultMCPTimeout {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.RetryAttempts != types.DefaultMCPRetryAttempts {
		t.Errorf("RetryAttempts = %d", c.RetryAttempts)
	}
}
