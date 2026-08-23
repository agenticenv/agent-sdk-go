package interfaces

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

//go:generate mockgen -destination=./mocks/mock_tool.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/interfaces Tool,ToolApproval,ToolAuthorizer

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

	// DisplayName returns the tool display name (e.g. "Search", "Calculator"). Used by the LLM in tool calls.
	DisplayName() string

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

// ToolExecMeta carries per-invocation metadata the agent sets on the context before calling
// [Tool.Execute]. Tool authors can read it via [ToolExecMetaFromContext] to forward idempotency
// keys to external APIs, enrich logs, or correlate traces.
//
// Fields may be extended in future SDK versions; always use named access rather than position.
type ToolExecMeta struct {
	// IdempotencyKey is a stable, unique key for this tool invocation suitable for forwarding
	// to non-idempotent external APIs. It is derived from RunID, Iteration, and ToolCallID and
	// survives Temporal ContinueAsNew boundaries for the same logical tool call.
	IdempotencyKey string
	// RunID is the agent run identifier.
	RunID string
	// ToolCallID is the identifier the LLM assigned to this tool call.
	ToolCallID string
	// Iteration is the LLM round (zero-based) in which this tool call was made.
	Iteration int
}

type toolExecMetaKey struct{}

// WithToolExecMeta returns a copy of ctx carrying meta. Called by the agent runtime before
// invoking [Tool.Execute]; not intended for direct use by tool authors.
func WithToolExecMeta(ctx context.Context, meta ToolExecMeta) context.Context {
	return context.WithValue(ctx, toolExecMetaKey{}, meta)
}

// ToolExecMetaFromContext returns the [ToolExecMeta] set by the agent runtime, and whether it
// was present. Returns a zero-value ToolExecMeta and false when called outside an agent tool
// execution (e.g. in unit tests that call Execute directly).
func ToolExecMetaFromContext(ctx context.Context) (ToolExecMeta, bool) {
	meta, ok := ctx.Value(toolExecMetaKey{}).(ToolExecMeta)
	return meta, ok
}
