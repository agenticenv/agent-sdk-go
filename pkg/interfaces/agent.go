package interfaces

// AgentToolApprovalPolicy determines if a tool execution requires approval.
// Implement for custom behavior. Built-in policies: agent.RequireAllToolApprovalPolicy (default),
// agent.AutoToolApprovalPolicy(), agent.AllowlistToolApprovalPolicy(names...).
type AgentToolApprovalPolicy interface {
	RequiresApproval(tool Tool) bool
}
