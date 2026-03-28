package agent

import (
	"testing"

	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
)

func TestToolApprovalMetadata_regularTool(t *testing.T) {
	cfg := &agentConfig{
		Name:  "Main agent",
		tools: []interfaces.Tool{mockTool{name: "echo"}},
	}
	aw := &AgentWorker{config: cfg}
	kind, agent, delegate := toolApprovalMetadata(aw, "echo")
	if kind != ToolApprovalKindTool || agent != "Main agent" || delegate != "" {
		t.Fatalf("got kind=%q agent=%q delegate=%q", kind, agent, delegate)
	}
}

func TestToolApprovalMetadata_delegation(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "MathPro"}}
	cfg := &agentConfig{
		Name:      "Main agent",
		subAgents: []*Agent{sub},
	}
	aw := &AgentWorker{config: cfg}
	toolName := SubAgentToolName(sub)
	kind, agent, delegate := toolApprovalMetadata(aw, toolName)
	if kind != ToolApprovalKindDelegation {
		t.Fatalf("kind = %q, want delegation", kind)
	}
	if agent != "Main agent" {
		t.Fatalf("agent = %q", agent)
	}
	if delegate != "MathPro" {
		t.Fatalf("delegate = %q, want MathPro", delegate)
	}
}
