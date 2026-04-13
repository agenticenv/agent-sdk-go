package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
)

var _ AgentTool = (*subAgentTool)(nil)
var _ interfaces.Tool = (*subAgentTool)(nil)

// Sub-agent tool names must be identifier-like for LLM tool APIs; normalize display names accordingly.
var subAgentToolNameNonIdent = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// defaultMaxSubAgentDepth is the maximum number of sub-agent hops from this agent when unset.
const defaultMaxSubAgentDepth = 2

// ErrSubAgentToolNotExecutable is returned by SubAgentTool.Execute.
// Normal runs delegate via AgentWorkflow child workflows; Execute() is only for misconfigured or direct activity calls.
var ErrSubAgentToolNotExecutable = errors.New("sub-agent tool must be executed via workflow, not Execute()")

// ErrSubAgentNameInvalid is returned when computing a sub-agent delegation tool name from a display name fails
// for a delegation tool (empty name, or name contains no letters or digits after normalization).
var ErrSubAgentNameInvalid = errors.New("sub-agent name invalid for delegation tool")

// AgentTool marks a tool that represents sub-agent delegation (child AgentWorkflow), not normal Tool.Execute.
//
// AgentWorkflow chooses delegation vs AgentToolExecuteActivity using SubAgentRoutes keyed by tool name, not by
// asserting AgentTool in workflow code. AgentTool is still used elsewhere (e.g. toolApprovalMetadata walks
// toolsList and asserts AgentTool to set delegation fields on approval events).
type AgentTool interface {
	interfaces.Tool
	// SubAgent returns the sub-agent this tool delegates to.
	SubAgent() *Agent
}

// subAgentTool implements interfaces.Tool for LLM tool listing only.
// Execution is handled by the agent workflow (child workflow), not Execute.
type subAgentTool struct {
	agent *Agent
	name  string // delegation tool name at construction
}

// subAgentToolName returns the registered delegation tool name for a sub-agent display name (typically [Agent.Name]
// from [WithName]). Single source: [NewSubAgentTool], validateToolNames, and buildSubAgentRoutes use this (or [subAgentTool.Name]).
//
// The name is not used verbatim: runs of non-alphanumeric ASCII become a single underscore; leading/trailing
// underscores are trimmed. Empty input or a name with no letters or digits after normalization is rejected ([ErrSubAgentNameInvalid]).
func subAgentToolName(name string) (string, error) {
	base := strings.TrimSpace(name)
	if base == "" {
		return "", fmt.Errorf("%w: set WithName on the sub-agent", ErrSubAgentNameInvalid)
	}
	safe := strings.Trim(subAgentToolNameNonIdent.ReplaceAllString(base, "_"), "_")
	if safe == "" {
		return "", fmt.Errorf("%w: name %q must contain at least one letter or digit", ErrSubAgentNameInvalid, base)
	}
	return "subagent_" + safe, nil
}

// NewSubAgentTool wraps a sub-agent as a tool for the parent LLM.
// The sub-agent must have a non-empty [Agent.Name] that yields at least one letter or digit after normalization
// (same normalization as delegation tool names from display names). Returns nil if sub is nil or the name is invalid.
func NewSubAgentTool(sub *Agent) interfaces.Tool {
	if sub == nil {
		return nil
	}
	n, err := subAgentToolName(sub.Name)
	if err != nil {
		return nil
	}
	return &subAgentTool{agent: sub, name: n}
}

func (t *subAgentTool) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

func (t *subAgentTool) Description() string {
	d := strings.TrimSpace(t.agent.Description)
	if d != "" {
		return "Delegate to specialist sub-agent: " + d
	}
	if strings.TrimSpace(t.agent.Name) != "" {
		return "Delegate to specialist sub-agent: " + t.agent.Name
	}
	return "Delegate to a specialist sub-agent to handle this request."
}

func (t *subAgentTool) Parameters() interfaces.JSONSchema {
	return tools.Params(map[string]interfaces.JSONSchema{
		types.SubAgentToolParamQuery: tools.ParamString("Task or question to send to the sub-agent."),
	}, types.SubAgentToolParamQuery)
}

func (t *subAgentTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	if t == nil {
		return nil, ErrSubAgentToolNotExecutable
	}
	return nil, fmt.Errorf("%w: %s", ErrSubAgentToolNotExecutable, t.name)
}

func (t *subAgentTool) SubAgent() *Agent { return t.agent }
