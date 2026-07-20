package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/mcp"
	"golang.org/x/oauth2/clientcredentials"
)

// MCPRootConfig holds optional MCP server definitions for the CLI.
type MCPRootConfig struct {
	Servers []MCPServerYAML `yaml:"servers"`
}

// MCPServerYAML is one MCP server entry under mcp.servers.
type MCPServerYAML struct {
	Enabled *bool `yaml:"enabled"`

	Name      string `yaml:"name"`
	Transport string `yaml:"transport"` // stdio | streamable_http (aliases: local | http | remote | streamable)

	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`

	URL           string            `yaml:"url"`
	BearerToken   string            `yaml:"bearer_token"`
	OAuthClientID string            `yaml:"oauth_client_id"`
	OAuthSecret   string            `yaml:"oauth_client_secret"`
	OAuthTokenURL string            `yaml:"oauth_token_url"`
	SkipTLSVerify bool              `yaml:"skip_tls_verify"`
	Headers       map[string]string `yaml:"headers"`

	TimeoutSeconds int      `yaml:"timeout_seconds"`
	RetryAttempts  int      `yaml:"retry_attempts"`
	AllowTools     []string `yaml:"allow_tools"`
	BlockTools     []string `yaml:"block_tools"`
}

func mcpServerEntryEnabled(e *MCPServerYAML) bool {
	if e == nil {
		return false
	}
	if e.Enabled == nil {
		return true
	}
	return *e.Enabled
}

func (e *MCPServerYAML) hasOAuthCreds() bool {
	if e == nil {
		return false
	}
	return strings.TrimSpace(e.OAuthClientID) != "" &&
		strings.TrimSpace(e.OAuthSecret) != "" &&
		strings.TrimSpace(e.OAuthTokenURL) != ""
}

// BuildMCPServers returns agent.MCPServers from config (nil map if none enabled).
func BuildMCPServers(cfg *Config) (agent.MCPServers, error) {
	if cfg == nil || cfg.MCP == nil || len(cfg.MCP.Servers) == 0 {
		return nil, nil
	}
	out := make(agent.MCPServers)
	seen := make(map[string]struct{})
	for i, raw := range cfg.MCP.Servers {
		if !mcpServerEntryEnabled(&raw) {
			continue
		}
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			return nil, fmt.Errorf("mcp.servers[%d]: name is required when enabled", i)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("mcp: duplicate server name %q", name)
		}
		seen[name] = struct{}{}

		tKind := strings.TrimSpace(strings.ToLower(raw.Transport))
		var transport mcp.MCPTransportConfig
		switch tKind {
		case "stdio", "local":
			if strings.TrimSpace(raw.Command) == "" {
				return nil, fmt.Errorf("mcp.servers[%d] (%s): command is required for stdio", i, name)
			}
			tr := mcp.MCPStdio{
				Command: strings.TrimSpace(raw.Command),
				Args:    raw.Args,
				Env:     raw.Env,
			}
			if err := tr.Validate(); err != nil {
				return nil, fmt.Errorf("mcp.servers[%d] (%s): %w", i, name, err)
			}
			transport = tr
		case "streamable_http", "http", "remote", "streamable":
			if strings.TrimSpace(raw.URL) == "" {
				return nil, fmt.Errorf("mcp.servers[%d] (%s): url is required for streamable_http", i, name)
			}
			tt := mcp.MCPStreamableHTTP{
				URL:           strings.TrimSpace(raw.URL),
				SkipTLSVerify: raw.SkipTLSVerify,
				Headers:       raw.Headers,
			}
			if raw.hasOAuthCreds() {
				if strings.TrimSpace(raw.BearerToken) != "" {
					return nil, fmt.Errorf("mcp.servers[%d] (%s): set only one of bearer_token or oauth credentials", i, name)
				}
				tt.OAuthClientCreds = &clientcredentials.Config{
					ClientID:     strings.TrimSpace(raw.OAuthClientID),
					ClientSecret: strings.TrimSpace(raw.OAuthSecret),
					TokenURL:     strings.TrimSpace(raw.OAuthTokenURL),
				}
			} else if strings.TrimSpace(raw.BearerToken) != "" {
				tt.Token = strings.TrimSpace(raw.BearerToken)
			}
			if err := tt.Validate(); err != nil {
				return nil, fmt.Errorf("mcp.servers[%d] (%s): %w", i, name, err)
			}
			transport = tt
		default:
			return nil, fmt.Errorf("mcp.servers[%d] (%s): unknown transport %q (use stdio or streamable_http)", i, name, raw.Transport)
		}

		mc := agent.MCPConfig{Transport: transport}
		if len(raw.AllowTools) > 0 || len(raw.BlockTools) > 0 {
			mc.ToolFilter = mcp.MCPToolFilter{AllowTools: raw.AllowTools, BlockTools: raw.BlockTools}
			if err := mc.ToolFilter.Validate(); err != nil {
				return nil, fmt.Errorf("mcp.servers[%d] (%s): tool filter: %w", i, name, err)
			}
		}
		if raw.TimeoutSeconds > 0 {
			mc.Timeout = time.Duration(raw.TimeoutSeconds) * time.Second
		}
		if raw.RetryAttempts > 0 {
			mc.RetryAttempts = raw.RetryAttempts
		}
		out[name] = mc
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
