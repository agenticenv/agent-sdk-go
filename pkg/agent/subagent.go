package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools"
)

var _ AgentTool = (*subAgentTool)(nil)
var _ interfaces.Tool = (*subAgentTool)(nil)

// subAgentToolParamQuery is the parameter name for the query to send to the sub-agent.
const subAgentToolParamQuery = "query"

// Sub-agent tool names must be identifier-like for LLM tool APIs; normalize display names accordingly.
var subAgentToolNameNonIdent = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// defaultMaxSubAgentDepth is the maximum number of sub-agent hops from this agent when unset.
const defaultMaxSubAgentDepth = 2

// ErrSubAgentToolNotExecutable is returned by SubAgentTool.Execute.
// Normal runs delegate via AgentWorkflow child workflows; Execute() is only for misconfigured or direct activity calls.
var ErrSubAgentToolNotExecutable = errors.New("sub-agent tool must be executed via workflow, not Execute()")

// AgentTool marks a tool that represents sub-agent delegation (child AgentWorkflow), not normal Tool.Execute.
//
// AgentWorkflow chooses delegation vs AgentToolExecuteActivity using SubAgentRoutes keyed by tool name, not by
// asserting AgentTool in workflow code. AgentTool is still used elsewhere (e.g. toolApprovalMetadata walks
// toolsList() and asserts AgentTool to set delegation fields on approval events).
type AgentTool interface {
	interfaces.Tool
	// SubAgent returns the sub-agent this tool delegates to.
	SubAgent() *Agent
}

// subAgentTool implements interfaces.Tool for LLM tool listing only.
// Execution is handled by the agent workflow (child workflow), not Execute.
type subAgentTool struct {
	agent *Agent
}

// NewSubAgentTool wraps a sub-agent as a tool for the parent LLM.
// Name is derived from the sub-agent's Name when set; otherwise a stable default is used.
// Returns nil if sub is nil; callers should skip nil tools.
func NewSubAgentTool(sub *Agent) interfaces.Tool {
	if sub == nil {
		return nil
	}
	return &subAgentTool{agent: sub}
}

func (t *subAgentTool) Name() string { return SubAgentToolName(t.agent) }

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
		subAgentToolParamQuery: tools.ParamString("Task or question to send to the sub-agent."),
	}, subAgentToolParamQuery)
}

func (t *subAgentTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	return nil, fmt.Errorf("%w: %s", ErrSubAgentToolNotExecutable, SubAgentToolName(t.agent))
}

func (t *subAgentTool) SubAgent() *Agent { return t.agent }

// SubAgentToolName returns the tool name the parent LLM and workflow routing use for this sub-agent.
// Single source: NewSubAgentTool, validateToolsAndSubAgentNames, and buildSubAgentRoutes must use this (or Name() on the tool).
//
// Display names are not used verbatim: runs of non-alphanumeric ASCII become a single underscore; leading/trailing
// underscores are trimmed. An empty or all-symbol name falls back to subagent_<Agent.ID> so the name stays stable and API-safe.
func SubAgentToolName(a *Agent) string {
	if a == nil {
		return ""
	}
	base := strings.TrimSpace(a.Name)
	if base == "" {
		return "subagent_" + a.ID
	}
	safe := strings.Trim(subAgentToolNameNonIdent.ReplaceAllString(base, "_"), "_")
	if safe == "" {
		return "subagent_" + a.ID
	}
	return "subagent_" + safe
}
