package config

import "strings"

// ForShow returns a copy of the effective merged config for `agctl config show`
// (defaults→xdg→user-file→env→flags), with secrets redacted. Temporal is only
// included when that runtime is selected; empty MCP is omitted.
func ForShow(cfg *Config) *Config {
	if cfg == nil {
		return &Config{}
	}
	out := &Config{
		Runtime:      cfg.Runtime,
		Name:         cfg.Name,
		SystemPrompt: cfg.SystemPrompt,
		LLMUsage:     cfg.LLMUsage,
	}

	if cfg.UseTemporalRuntime() && cfg.Temporal != nil {
		t := *cfg.Temporal
		out.Temporal = &t
	}

	if cfg.LLM != nil {
		l := *cfg.LLM
		if strings.TrimSpace(l.APIKey) != "" {
			l.APIKey = "***"
		} else {
			l.APIKey = ""
		}
		out.LLM = &l
	}

	if cfg.Logger != nil {
		l := *cfg.Logger
		if cfg.Logger.AddSource != nil {
			v := *cfg.Logger.AddSource
			l.AddSource = &v
		}
		out.Logger = &l
	}

	if cfg.Conversation != nil {
		c := *cfg.Conversation
		store := strings.ToLower(strings.TrimSpace(c.Store))
		if store == "redis" && cfg.Conversation.Redis != nil {
			r := *cfg.Conversation.Redis
			if strings.TrimSpace(r.Password) != "" {
				r.Password = "***"
			}
			c.Redis = &r
		} else {
			c.Redis = nil
		}
		out.Conversation = &c
	}

	if cfg.MCP != nil && len(cfg.MCP.Servers) > 0 {
		servers := make([]MCPServerYAML, len(cfg.MCP.Servers))
		for i, raw := range cfg.MCP.Servers {
			s := raw
			if strings.TrimSpace(s.BearerToken) != "" {
				s.BearerToken = "***"
			}
			if strings.TrimSpace(s.OAuthSecret) != "" {
				s.OAuthSecret = "***"
			}
			servers[i] = s
		}
		out.MCP = &MCPRootConfig{Servers: servers}
	}

	if len(cfg.Tools) > 0 {
		tools := make(map[string]ToolConfig, len(cfg.Tools))
		for name, t := range cfg.Tools {
			tools[name] = ToolConfig{Enabled: t.Enabled}
		}
		out.Tools = tools
	}

	return out
}
