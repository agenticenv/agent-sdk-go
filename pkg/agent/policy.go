package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// toolPolicyFingerprint returns a stable string for temporal.ComputeAgentFingerprint so caller
// and worker processes agree on approval semantics.
func toolPolicyFingerprint(p interfaces.AgentToolApprovalPolicy) string {
	if p == nil {
		return "nil"
	}
	switch x := p.(type) {
	case RequireAllToolApprovalPolicy:
		return "require_all"
	case *RequireAllToolApprovalPolicy:
		return "require_all"
	case autoToolApprovalPolicy:
		return "auto"
	case allowlistToolApprovalPolicy:
		names := make([]string, 0, len(x.allowed))
		for n := range x.allowed {
			names = append(names, n)
		}
		sort.Strings(names)
		return "allowlist:" + strings.Join(names, ",")
	default:
		return fmt.Sprintf("unknown:%T", p)
	}
}

// RequireAllToolApprovalPolicy requires approval for every tool. Default when no policy is set.
type RequireAllToolApprovalPolicy struct{}

func (RequireAllToolApprovalPolicy) RequiresApproval(interfaces.Tool) bool { return true }

// AutoToolApprovalPolicy allows all tools without approval. Use when you trust the agent.
func AutoToolApprovalPolicy() interfaces.AgentToolApprovalPolicy { return autoToolApprovalPolicy{} }

type autoToolApprovalPolicy struct{}

func (autoToolApprovalPolicy) RequiresApproval(t interfaces.Tool) bool { return false }

// AllowlistToolApprovalConfig selects tools and capabilities that may run without user approval.
// Entries are expanded to registered [interfaces.Tool] names (what [interfaces.Tool.Name] returns);
// [allowlistToolApprovalPolicy.RequiresApproval] compares against those names.
type AllowlistToolApprovalConfig struct {
	// ToolNames are plain registry / [WithTools] names (e.g. "echo", "calculator").
	ToolNames []string
	// SubAgentNames are trimmed sub-agent display names ([WithName]) for agents registered with [WithSubAgents].
	// Each entry is expanded with the same rules as sub-agent delegation tool names (see [NewSubAgentTool]). Invalid names make
	// [AllowlistToolApprovalPolicy] return an error.
	SubAgentNames []string
	// MCPTools maps MCP server key (same as [MCPTool.ServerName] / MCP config) to server tool ids
	// ([interfaces.ToolSpec.Name] as returned by the server). Each pair expands to mcp_<server>_<toolId>.
	MCPTools map[string][]string
}

// AllowlistToolApprovalPolicy builds an allowlist policy from [AllowlistToolApprovalConfig].
// Empty [AllowlistToolApprovalConfig.ToolNames] and MCP server/tool entries are skipped.
// Invalid [AllowlistToolApprovalConfig.SubAgentNames] entries return an error.
// MCP map iteration order does not affect behavior.
//
// Tool-only allowlisting: AllowlistToolApprovalPolicy(AllowlistToolApprovalConfig{ToolNames: []string{"echo", "calculator"}}).
func AllowlistToolApprovalPolicy(c AllowlistToolApprovalConfig) (interfaces.AgentToolApprovalPolicy, error) {
	m := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" {
			m[s] = true
		}
	}
	for _, n := range c.ToolNames {
		add(n)
	}
	for _, n := range c.SubAgentNames {
		name, err := subAgentToolName(n)
		if err != nil {
			return nil, fmt.Errorf("allowlist SubAgentNames entry %q: %w", n, err)
		}
		add(name)
	}
	for server, tools := range c.MCPTools {
		for _, tid := range tools {
			add(mcpToolName(server, tid))
		}
	}
	return allowlistToolApprovalPolicy{m}, nil
}

type allowlistToolApprovalPolicy struct {
	allowed map[string]bool
}

func (p allowlistToolApprovalPolicy) RequiresApproval(t interfaces.Tool) bool {
	return !p.allowed[t.Name()]
}

var _ interfaces.AgentToolApprovalPolicy = (*RequireAllToolApprovalPolicy)(nil)
var _ interfaces.AgentToolApprovalPolicy = (*autoToolApprovalPolicy)(nil)
var _ interfaces.AgentToolApprovalPolicy = (*allowlistToolApprovalPolicy)(nil)
