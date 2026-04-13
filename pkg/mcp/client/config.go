package client

import (
	"fmt"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/mcp"
)

// ClientConfig holds optional settings for [NewClient].
// Logger and LogLevel mirror pkg/llm.LLMConfig; Timeout, RetryAttempts, and ToolFilter align with [github.com/agenticenv/agent-sdk-go/pkg/agent.MCPConfig] (same field semantics as [github.com/agenticenv/agent-sdk-go/pkg/agent.MCPConfig.ToolFilter] / [mcp.MCPToolFilter]).
type ClientConfig struct {
	Logger        logger.Logger
	LogLevel      string
	Timeout       time.Duration // per Ping/ListTools/CallTool: deadline for connect+RPC in one attempt
	RetryAttempts int           // max attempts per operation (connect+fn); default matches [types.DefaultMCPRetryAttempts]
	ToolFilter    mcp.MCPToolFilter
}

// Option mutates [ClientConfig] when passed to [BuildConfig] or [NewClient].
type Option func(*ClientConfig)

// WithLogger sets diagnostics for this MCP client and, when l is [*logger.SlogLogger], forwards the
// underlying [*log/slog.Logger] to the go-sdk MCP client for protocol-level logs.
func WithLogger(l logger.Logger) Option {
	return func(c *ClientConfig) {
		c.Logger = l
	}
}

// WithLogLevel sets the level used when [WithLogger] is not set (same strings as [logger.DefaultLogger]: debug, info, warn, error).
// Empty defaults to "error" in [BuildConfig].
func WithLogLevel(level string) Option {
	return func(c *ClientConfig) {
		c.LogLevel = level
	}
}

// WithTimeout sets the per-operation deadline for each attempt (connect plus one RPC chain, e.g. full ListTools pagination).
// Zero means use [types.DefaultMCPTimeout] in [BuildConfig].
func WithTimeout(d time.Duration) Option {
	return func(c *ClientConfig) {
		c.Timeout = d
	}
}

// WithRetryAttempts sets how many times [Client] may repeat connect+RPC after a failure (minimum 1 after [BuildConfig]).
// Zero means use [types.DefaultMCPRetryAttempts] in [BuildConfig].
func WithRetryAttempts(n int) Option {
	return func(c *ClientConfig) {
		c.RetryAttempts = n
	}
}

// WithToolFilter sets allow/block lists (same semantics as [github.com/agenticenv/agent-sdk-go/pkg/agent.MCPConfig.ToolFilter]).
// [BuildConfig] validates the filter; [Client.ListTools] runs [mcp.MCPToolFilter.Apply] to restrict returned specs.
func WithToolFilter(f mcp.MCPToolFilter) Option {
	return func(c *ClientConfig) {
		c.ToolFilter = f
	}
}

// BuildConfig builds [ClientConfig] from options. Defaults when not set:
//   - LogLevel: "error"
//   - Logger: stderr slog logger at LogLevel
//
// It returns an error if [ClientConfig.ToolFilter] fails [mcp.MCPToolFilter.Validate].
func BuildConfig(opts ...Option) (*ClientConfig, error) {
	c := &ClientConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.LogLevel == "" {
		c.LogLevel = "error"
	}
	if c.Logger == nil {
		c.Logger = logger.DefaultLogger(c.LogLevel)
	}
	if c.Timeout <= 0 {
		c.Timeout = types.DefaultMCPTimeout
	}
	if c.RetryAttempts <= 0 {
		c.RetryAttempts = types.DefaultMCPRetryAttempts
	}
	if err := c.ToolFilter.Validate(); err != nil {
		return nil, fmt.Errorf("mcp client config: %w", err)
	}
	return c, nil
}
