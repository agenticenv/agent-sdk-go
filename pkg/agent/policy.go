package agent

import (
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"
)

// RequireAllToolApprovalPolicy requires approval for every tool. Default when no policy is set.
type RequireAllToolApprovalPolicy struct{}

func (RequireAllToolApprovalPolicy) RequiresApproval(interfaces.Tool) bool { return true }

// AutoToolApprovalPolicy allows all tools without approval. Use when you trust the agent.
func AutoToolApprovalPolicy() interfaces.AgentToolApprovalPolicy { return autoToolApprovalPolicy{} }

type autoToolApprovalPolicy struct{}

func (autoToolApprovalPolicy) RequiresApproval(t interfaces.Tool) bool { return false }

// AllowlistToolApprovalPolicy allows only the listed tools without approval; all others require approval.
func AllowlistToolApprovalPolicy(toolNames ...string) interfaces.AgentToolApprovalPolicy {
	m := make(map[string]bool)
	for _, n := range toolNames {
		m[n] = true
	}
	return allowlistToolApprovalPolicy{m}
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
