package interfaces

//go:generate mockgen -destination=./mocks/mock_agent.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/interfaces AgentToolApprovalPolicy

// AgentToolApprovalPolicy determines if a tool execution requires approval.
// Implement for custom behavior. Built-in policies: agent.RequireAllToolApprovalPolicy (default),
// agent.AutoToolApprovalPolicy(), agent.AllowlistToolApprovalPolicy(agent.AllowlistToolApprovalConfig{...}) (may error on invalid sub-agent names).
type AgentToolApprovalPolicy interface {
	RequiresApproval(tool Tool) bool
}
