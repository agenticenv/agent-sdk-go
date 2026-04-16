package interfaces

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

//go:generate mockgen -destination=./mocks/mock_tool.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/interfaces Tool,ToolRegistry,ToolApproval,ToolAuthorizer

// ToolApproval is an optional interface for tools that require interactive human approval before execution.
// When implemented, the agent honors ApprovalRequired() when no agent-level approval policy is set.
// WithToolApprovalPolicy overrides this tool-level default when set.
type ToolApproval interface {
	ApprovalRequired() bool
}

// ToolAuthorizer is an optional interface for tools that enforce programmatic authorization.
// When implemented, the agent checks Authorize before approval/Execute in the tool call flow.
// Return a decision with Allow=true/false and optional deny metadata.
type ToolAuthorizer interface {
	Authorize(ctx context.Context, args map[string]any) (ToolAuthorizationDecision, error)
}

// ToolAuthorizationDecision is the structured authorization outcome for one tool call.
// Reason is optional and primarily useful when Allow is false.
type ToolAuthorizationDecision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// Tool is a callable capability the agent can offer to the LLM. Register tools via agent.WithTools.
// The LLM receives tool definitions and chooses which to call; the agent executes the chosen tool.
type Tool interface {
	// Name returns the tool identifier (e.g. "search", "calculator"). Used by the LLM in tool calls.
	Name() string

	// Description describes when and how to use this tool. Shown to the LLM for tool selection.
	Description() string

	// Parameters returns the JSON schema for the tool's input. The LLM produces args matching this schema.
	// Use tools.Params with tools.ParamString, ParamInteger, etc. for type-safe construction.
	Parameters() JSONSchema

	// Execute runs the tool with the given args. Args match the Parameters schema.
	// Called by the agent when the LLM returns a tool call for this tool.
	Execute(ctx context.Context, args map[string]any) (any, error)
}

// ToolSpec is the schema sent to the LLM for tool selection (canonical definition in [types.ToolSpec]).
type ToolSpec = types.ToolSpec

// JSONSchema is a loose JSON Schema object for tool parameters (canonical definition in [types.JSONSchema]).
type JSONSchema = types.JSONSchema

// ToolToSpec converts a Tool to its spec for the LLM.
func ToolToSpec(t Tool) ToolSpec {
	return ToolSpec{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters:  t.Parameters(),
	}
}

// ToolsToSpecs converts a slice of Tool to specs for the LLM.
func ToolsToSpecs(tools []Tool) []ToolSpec {
	specs := make([]ToolSpec, len(tools))
	for i, t := range tools {
		specs[i] = ToolToSpec(t)
	}
	return specs
}

// ToolRegistry manages a collection of tools. Use for registering and looking up tools by name.
type ToolRegistry interface {
	// Register adds a tool. Overwrites if a tool with the same name exists.
	Register(tool Tool)

	// Get returns the tool by name, or (nil, false) if not found.
	Get(name string) (Tool, bool)

	// Tools returns all registered tools in registration order.
	Tools() []Tool
}
