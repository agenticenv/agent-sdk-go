// Package mcp defines transport and tool-filter shapes for MCP server connections.
//
// Use these types with [github.com/agenticenv/agent-sdk-go/pkg/agent.WithMCPConfig] ([github.com/agenticenv/agent-sdk-go/pkg/agent.MCPConfig].Transport and ToolFilter),
// [github.com/agenticenv/agent-sdk-go/pkg/mcp/client.NewClient], and related APIs. Definitions live in internal/types/mcp.go.
package mcp

import "github.com/agenticenv/agent-sdk-go/internal/types"

// Type aliases for MCP transport and tool filtering ([github.com/agenticenv/agent-sdk-go/pkg/agent.MCPConfig], client constructors).

type (
	MCPTransportConfig = types.MCPTransportConfig
	MCPTransportType   = types.MCPTransportType
	MCPStdio           = types.MCPStdio
	MCPStreamableHTTP  = types.MCPStreamableHTTP
	MCPToolFilter      = types.MCPToolFilter
)

const (
	MCPTransportTypeStdio          = types.MCPTransportTypeStdio
	MCPTransportTypeStreamableHTTP = types.MCPTransportTypeStreamableHTTP
)
