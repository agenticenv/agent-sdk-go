package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

var (
	mcpToolNameTemplate = "mcp_%s_%s"
)

var _ interfaces.Tool = (*MCPTool)(nil)

// NOTE: MCPTools for the same server share one MCPClient. The default pkg/mcp/client serializes
// RPCs on that client with a mutex; custom MCPClient implementations should document concurrency behavior.

// MCPTool implements interfaces.Tool for one MCP tool on a server. Execute delegates to
// MCPClient.CallTool using Spec.Name as the server tool name.
type MCPTool struct {
	ServerName string
	Spec       interfaces.ToolSpec
	Client     interfaces.MCPClient
}

// mcpToolName returns the registered tool name for an MCP server key and server tool id
// (same format as [MCPTool.Name]). Trims whitespace on both inputs; returns "" if either is empty after trim.
func mcpToolName(serverName, toolName string) string {
	sn := strings.TrimSpace(serverName)
	tn := strings.TrimSpace(toolName)
	if sn == "" || tn == "" {
		return ""
	}
	return fmt.Sprintf(mcpToolNameTemplate, sn, tn)
}

// NewMCPTool builds an MCPTool. When spec.Parameters is nil, [MCPTool.Parameters] returns a default object schema.
func NewMCPTool(serverName string, spec interfaces.ToolSpec, client interfaces.MCPClient) *MCPTool {
	return &MCPTool{
		ServerName: serverName,
		Spec:       spec,
		Client:     client,
	}
}

// Name implements interfaces.Tool.
func (m *MCPTool) Name() string {
	if m == nil {
		return ""
	}
	return mcpToolName(m.ServerName, m.Spec.Name)
}

// Description implements interfaces.Tool.
func (m *MCPTool) Description() string {
	if m == nil {
		return ""
	}
	return m.Spec.Description
}

// Parameters implements interfaces.Tool.
func (m *MCPTool) Parameters() interfaces.JSONSchema {
	if m == nil || m.Spec.Parameters == nil {
		return interfaces.JSONSchema{"type": "object"}
	}
	return m.Spec.Parameters
}

// Execute implements interfaces.Tool: marshal args, CallTool, return decoded JSON or raw string.
func (m *MCPTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	if m == nil || m.Client == nil {
		return nil, fmt.Errorf("mcp tool: nil client")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	out, err := m.Client.CallTool(ctx, m.Spec.Name, raw)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return "", nil
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return string(out), nil
	}
	return v, nil
}

// mcpConfigFingerprint returns a stable digest of MCP wiring for temporal.ComputeAgentFingerprint:
// server keys, transport identity (stdio command/args, HTTP URL and header keys only — no secrets),
// timeouts, retries, tool filters, and sorted extra MCP client names (from WithMCPClients).
// Empty when there are no MCP servers and no extra client names.
func mcpConfigFingerprint(servers MCPServers, extraClientNames []string) string {
	if len(servers) == 0 && len(extraClientNames) == 0 {
		return ""
	}
	shot := mcpFpShot{}
	keys := make([]string, 0, len(servers))
	for k := range servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cfg := servers[k]
		to := cfg.Timeout
		if to == 0 {
			to = types.DefaultMCPTimeout
		}
		ra := cfg.RetryAttempts
		if ra == 0 {
			ra = types.DefaultMCPRetryAttempts
		}
		row := mcpFpServerRow{
			Key:       k,
			TimeoutNs: to.Nanoseconds(),
			Retries:   ra,
		}
		if len(cfg.ToolFilter.AllowTools) > 0 {
			row.AllowTools = append([]string(nil), cfg.ToolFilter.AllowTools...)
			sort.Strings(row.AllowTools)
		}
		if len(cfg.ToolFilter.BlockTools) > 0 {
			row.BlockTools = append([]string(nil), cfg.ToolFilter.BlockTools...)
			sort.Strings(row.BlockTools)
		}
		tr := cfg.Transport
		if tr == nil {
			row.Kind = "nil_transport"
			shot.Servers = append(shot.Servers, row)
			continue
		}
		row.Kind = string(tr.Kind())
		switch t := tr.(type) {
		case types.MCPStdio:
			args := append([]string(nil), t.Args...)
			var envKeys []string
			for ek := range t.Env {
				envKeys = append(envKeys, ek)
			}
			sort.Strings(envKeys)
			row.Stdio = &mcpFpStdio{Command: t.Command, Args: args, EnvKeys: envKeys}
		case types.MCPStreamableHTTP:
			var hdrKeys []string
			for hk := range t.Headers {
				if s := strings.TrimSpace(hk); s != "" {
					hdrKeys = append(hdrKeys, s)
				}
			}
			sort.Strings(hdrKeys)
			authLabel := "none"
			if err := t.Validate(); err != nil {
				authLabel = "invalid"
			} else if cc := t.OAuthClientCreds; cc != nil &&
				(strings.TrimSpace(cc.ClientID) != "" || strings.TrimSpace(cc.ClientSecret) != "" ||
					strings.TrimSpace(cc.TokenURL) != "" || len(cc.Scopes) > 0 || len(cc.EndpointParams) > 0) {
				authLabel = "oauth2_client_credentials"
			} else if strings.TrimSpace(t.Token) != "" {
				authLabel = "bearer_token"
			}
			var oauthEPKeys, oScopes []string
			var oauthTokURL string
			hasOAuthSecret := false
			if cc := t.OAuthClientCreds; cc != nil {
				oauthTokURL = strings.TrimSpace(cc.TokenURL)
				hasOAuthSecret = strings.TrimSpace(cc.ClientSecret) != ""
				oScopes = append([]string(nil), cc.Scopes...)
				sort.Strings(oScopes)
				for k := range cc.EndpointParams {
					if s := strings.TrimSpace(k); s != "" {
						oauthEPKeys = append(oauthEPKeys, s)
					}
				}
				sort.Strings(oauthEPKeys)
			}
			row.HTTP = &mcpFpHTTP{
				URL:                strings.TrimSpace(t.URL),
				AuthType:           authLabel,
				HasBearerToken:     strings.TrimSpace(t.Token) != "",
				OAuth2TokenURL:     oauthTokURL,
				OAuth2Scopes:       oScopes,
				HasOAuth2Secret:    hasOAuthSecret,
				OAuth2EndpointKeys: oauthEPKeys,
				ExtraHeaderKeys:    hdrKeys,
			}
		case types.MCPLoopback:
			row.Loopback = true
		default:
			row.Kind = "unknown"
		}
		shot.Servers = append(shot.Servers, row)
	}
	names := append([]string(nil), extraClientNames...)
	sort.Strings(names)
	shot.ExtraClients = names
	b, err := json.Marshal(shot)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mcpExtraClientNames returns sorted, non-empty MCP client names from WithMCPClients for mcpConfigFingerprint.
func mcpExtraClientNames(clients []interfaces.MCPClient) []string {
	var out []string
	for _, cl := range clients {
		if cl == nil {
			continue
		}
		if n := strings.TrimSpace(cl.Name()); n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

type mcpFpShot struct {
	Servers      []mcpFpServerRow `json:"servers,omitempty"`
	ExtraClients []string         `json:"extra_clients,omitempty"`
}

type mcpFpServerRow struct {
	Key        string      `json:"key"`
	Kind       string      `json:"kind"`
	Stdio      *mcpFpStdio `json:"stdio,omitempty"`
	HTTP       *mcpFpHTTP  `json:"http,omitempty"`
	Loopback   bool        `json:"loopback,omitempty"`
	TimeoutNs  int64       `json:"timeout_ns"`
	Retries    int         `json:"retries"`
	AllowTools []string    `json:"allow_tools,omitempty"`
	BlockTools []string    `json:"block_tools,omitempty"`
}

type mcpFpStdio struct {
	Command string   `json:"cmd"`
	Args    []string `json:"args,omitempty"`
	EnvKeys []string `json:"env_keys,omitempty"`
}

type mcpFpHTTP struct {
	URL                string   `json:"url"`
	AuthType           string   `json:"auth_type,omitempty"`
	HasBearerToken     bool     `json:"has_bearer,omitempty"`
	OAuth2TokenURL     string   `json:"oauth2_token_url,omitempty"`
	OAuth2Scopes       []string `json:"oauth2_scopes,omitempty"`
	HasOAuth2Secret    bool     `json:"has_oauth2_secret,omitempty"`
	OAuth2EndpointKeys []string `json:"oauth2_endpoint_keys,omitempty"`
	ExtraHeaderKeys    []string `json:"header_keys,omitempty"`
}
