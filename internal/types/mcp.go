package types

import (
	"fmt"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2/clientcredentials"
)

// Default MCP settings applied when fields are zero.
const (
	DefaultMCPTimeout       = 30 * time.Second
	DefaultMCPRetryAttempts = 3
)

// MCPToolFilter restricts which tools from Discover are registered (exact name match).
// Set either AllowTools (allow-list) or BlockTools (block-list), not both.
// [MCPToolFilter.Validate] checks constraints (call from config build, e.g. [github.com/agenticenv/agent-sdk-go/pkg/mcp/client.BuildConfig]).
// [MCPToolFilter.Apply] filters tool specs and assumes Validate already passed for non-empty filters.
type MCPToolFilter struct {
	AllowTools []string
	BlockTools []string
}

// Validate returns an error if both AllowTools and BlockTools are set.
func (f MCPToolFilter) Validate() error {
	if len(f.AllowTools) > 0 && len(f.BlockTools) > 0 {
		return fmt.Errorf("mcp tool filter: set either AllowTools or BlockTools, not both")
	}
	return nil
}

// Apply returns filtered specs when AllowTools or BlockTools is non-empty; otherwise returns specs unchanged.
// For any non-empty list, the receiver must already satisfy [MCPToolFilter.Validate] (mutually exclusive lists).
func (f MCPToolFilter) Apply(specs []ToolSpec) []ToolSpec {
	if len(f.AllowTools) > 0 {
		allow := make(map[string]struct{}, len(f.AllowTools))
		for _, n := range f.AllowTools {
			allow[n] = struct{}{}
		}
		out := make([]ToolSpec, 0, len(specs))
		for _, d := range specs {
			if _, ok := allow[d.Name]; ok {
				out = append(out, d)
			}
		}
		return out
	}
	if len(f.BlockTools) > 0 {
		drop := make(map[string]struct{}, len(f.BlockTools))
		for _, n := range f.BlockTools {
			drop[n] = struct{}{}
		}
		out := make([]ToolSpec, 0, len(specs))
		for _, d := range specs {
			if _, bad := drop[d.Name]; !bad {
				out = append(out, d)
			}
		}
		return out
	}
	return specs
}

type MCPTransportType string

const (
	MCPTransportTypeStdio          MCPTransportType = "stdio"
	MCPTransportTypeStreamableHTTP MCPTransportType = "streamable_http"
)

// MCPTransportConfig describes how to reach one MCP server. Concrete types are [MCPStdio], [MCPStreamableHTTP], and [MCPLoopback] (tests).
type MCPTransportConfig interface {
	// Kind returns a stable transport id ("stdio", "streamable_http") for logging and routing.
	Kind() MCPTransportType
	// Validate checks the transport is usable before connect (the default MCP client calls this from NewClient).
	Validate() error
}

// MCPStdio runs an MCP server as a subprocess (stdio).
type MCPStdio struct {
	Command string
	Args    []string
	Env     map[string]string
}

func (MCPStdio) Kind() MCPTransportType { return MCPTransportTypeStdio }

// Validate ensures Command is set.
func (s MCPStdio) Validate() error {
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("mcp stdio transport: Command is empty")
	}
	return nil
}

// MCPStreamableHTTP uses the streamable HTTP MCP transport.
//
// Optional static bearer Token, or OAuthClientCreds for OAuth2 client credentials.
// Token and active OAuth client-credentials must not both be set; omit both for URL-only access (use Headers for custom auth headers such as API keys).
type MCPStreamableHTTP struct {
	URL string
	// Token is a static bearer token when OAuthClientCreds is not used for auth.
	Token string
	// OAuthClientCreds configures OAuth2 client-credentials; when any OAuth field is set, Token must be empty and id/secret/token_url are required together.
	OAuthClientCreds *clientcredentials.Config
	Headers          map[string]string
	SkipTLSVerify    bool
}

func (MCPStreamableHTTP) Kind() MCPTransportType { return MCPTransportTypeStreamableHTTP }

// Validate checks URL, rejects mixing Token with a populated OAuth client-credentials config, and rejects incomplete OAuth when any OAuth field is set.
func (h MCPStreamableHTTP) Validate() error {
	if strings.TrimSpace(h.URL) == "" {
		return fmt.Errorf("mcp streamable_http: url is required")
	}
	hasTok := strings.TrimSpace(h.Token) != ""

	cc := h.OAuthClientCreds
	oauthActive := false
	if cc != nil {
		id := strings.TrimSpace(cc.ClientID)
		sec := strings.TrimSpace(cc.ClientSecret)
		tu := strings.TrimSpace(cc.TokenURL)
		anyOAuth := id != "" || sec != "" || tu != "" || len(cc.Scopes) > 0 || len(cc.EndpointParams) > 0
		if anyOAuth {
			if id == "" || sec == "" || tu == "" {
				return fmt.Errorf("mcp streamable_http: oauth client credentials require client_id, client_secret, and token_url")
			}
			oauthActive = true
		}
	}

	if hasTok && oauthActive {
		return fmt.Errorf("mcp streamable_http: set only one of token or oauth client credentials")
	}
	return nil
}

// MCPTransportTypeLoopback is only for in-repo tests (see MCPLoopback). Not exposed on the public agent API.
const MCPTransportTypeLoopback MCPTransportType = "loopback"

// MCPLoopback is test-only wiring: it holds a pre-built protocol transport as a dynamic value.
// External users should use pkg/mcp transport types (MCPStdio, MCPStreamableHTTP). MCPLoopback is not re-exported from pkg/mcp.
type MCPLoopback struct {
	Transport any
}

func (MCPLoopback) Kind() MCPTransportType { return MCPTransportTypeLoopback }

// Validate ensures Transport is a non-nil [sdkmcp.Transport].
func (lb MCPLoopback) Validate() error {
	tr, ok := lb.Transport.(sdkmcp.Transport)
	if !ok || tr == nil {
		return fmt.Errorf("mcp loopback transport: Transport must be a non-nil sdkmcp.Transport")
	}
	return nil
}
