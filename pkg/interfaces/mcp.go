package interfaces

import (
	"context"
	"encoding/json"
)

//go:generate mockgen -destination=./mocks/mock_mcp.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/interfaces MCPClient

// MCPClient is a client to one MCP server: optional reachability check, tools, optional close.
// Implementations may wrap modelcontextprotocol/go-sdk or other transports.
type MCPClient interface {
	// Name identifies this connection for logging and tool prefixes (e.g. [github.com/agenticenv/agent-sdk-go/pkg/agent.MCPServers] key or [github.com/agenticenv/agent-sdk-go/pkg/mcp/client.NewClient] first argument).
	Name() string
	// Ping checks that the server responds (MCP ping on a short-lived session). The default
	// implementation connects, pings, and disconnects; ListTools and CallTool each open their own session.
	Ping(ctx context.Context) error
	// ListTools returns tool definitions from the server (tools/list).
	ListTools(ctx context.Context) ([]ToolSpec, error)
	// CallTool invokes a tool by name with JSON arguments (tools/call).
	CallTool(ctx context.Context, tool string, input json.RawMessage) (json.RawMessage, error)
	// Close releases the connection or session.
	Close() error
}

// ResourceSpec is one entry from resources/list (subset of MCP; fields may grow with spec versions).
type ResourceSpec struct {
	URI         string `json:"uri"`                   // Resource URI.
	Name        string `json:"name,omitempty"`        // Human-readable name.
	Description string `json:"description,omitempty"` // Short description.
	MimeType    string `json:"mimeType,omitempty"`    // Optional MIME type hint.
}

// MCPResourceClient extends MCPClient with MCP resources. Optional: tool-only agents need only MCPClient.
type MCPResourceClient interface {
	MCPClient
	// ListResources returns available resources (resources/list).
	ListResources(ctx context.Context) ([]ResourceSpec, error)
	// ReadResource returns the resource body for uri (resources/read).
	ReadResource(ctx context.Context, uri string) (json.RawMessage, error)
}

// PromptSpec is one entry from prompts/list (subset of MCP; fields may grow with spec versions).
type PromptSpec struct {
	Name        string `json:"name"`                  // Prompt identifier.
	Description string `json:"description,omitempty"` // Short description.
}

// MCPPromptClient extends MCPClient with MCP prompts. Optional: tool-only agents need only MCPClient.
type MCPPromptClient interface {
	MCPClient
	// ListPrompts returns available prompts (prompts/list).
	ListPrompts(ctx context.Context) ([]PromptSpec, error)
	// GetPrompt resolves a prompt template with arguments (prompts/get).
	GetPrompt(ctx context.Context, name string, args map[string]string) (json.RawMessage, error)
}
