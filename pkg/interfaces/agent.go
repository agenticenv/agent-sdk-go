package interfaces

//go:generate mockgen -destination=./mocks/mock_agent.go -package=mocks github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces AgentToolApprovalPolicy

// AgentToolApprovalPolicy determines if a tool execution requires approval.
// Implement for custom behavior. Built-in policies: agent.RequireAllToolApprovalPolicy (default),
// agent.AutoToolApprovalPolicy(), agent.AllowlistToolApprovalPolicy(names...).
type AgentToolApprovalPolicy interface {
	RequiresApproval(tool Tool) bool
}
