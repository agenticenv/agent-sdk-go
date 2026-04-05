package agent

import (
	"context"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func TestCopyApprovalArgs(t *testing.T) {
	if copyApprovalArgs(nil) != nil {
		t.Error("copyApprovalArgs(nil) should return nil")
	}
	src := map[string]any{"a": 1, "b": "x"}
	dst := copyApprovalArgs(src)
	if dst == nil {
		t.Fatal("copyApprovalArgs should not return nil for non-nil input")
	}
	if dst["a"] != 1 || dst["b"] != "x" {
		t.Errorf("copyApprovalArgs = %v, want %v", dst, src)
	}
	// Ensure copy is independent (modify src, dst should be unchanged)
	src["c"] = 99
	if _, ok := dst["c"]; ok {
		t.Error("copyApprovalArgs should return a copy, not share the map")
	}
}

func TestAgent_ValidateConversationID(t *testing.T) {
	l := logger.DefaultLogger("error")
	a := &Agent{agentConfig: agentConfig{logger: l}}

	if err := a.validateConversationID(""); err != nil {
		t.Errorf("empty conversationID with no conversation: %v", err)
	}
	if err := a.validateConversationID("conv1"); err == nil {
		t.Error("non-empty conversationID with no conversation should error")
	}

	a.conversation = &mockConversation{}
	if err := a.validateConversationID(""); err == nil {
		t.Error("empty conversationID with conversation should error")
	}
	if err := a.validateConversationID("conv1"); err != nil {
		t.Errorf("valid conversationID with conversation: %v", err)
	}
}

type mockConversation struct{}

func (m *mockConversation) AddMessage(ctx context.Context, id string, msg interfaces.Message) error {
	return nil
}
func (m *mockConversation) ListMessages(ctx context.Context, id string, opts ...interfaces.ListMessagesOption) ([]interfaces.Message, error) {
	return nil, nil
}
func (m *mockConversation) Clear(ctx context.Context, id string) error { return nil }
func (m *mockConversation) IsDistributed() bool                        { return false }

func TestBuildWorkflowSubAgentRoutes_flat(t *testing.T) {
	child := &Agent{agentConfig: agentConfig{Name: "Child", taskQueue: "q-child"}}
	parent := &Agent{agentConfig: agentConfig{Name: "Parent", taskQueue: "q-parent", subAgents: []*Agent{child}}}
	got := parent.buildSubAgentRoutes()
	if got == nil {
		t.Fatal("expected routes")
	}
	key := SubAgentToolName(child)
	r, ok := got[key]
	if !ok {
		t.Fatalf("missing %q in %v", key, got)
	}
	if r.TaskQueue != "q-child" || r.ChildRoutes != nil {
		t.Fatalf("route = %+v", r)
	}
}

func TestBuildWorkflowSubAgentRoutes_nested(t *testing.T) {
	leaf := &Agent{agentConfig: agentConfig{Name: "Leaf", taskQueue: "q-leaf"}}
	mid := &Agent{agentConfig: agentConfig{Name: "Mid", taskQueue: "q-mid", subAgents: []*Agent{leaf}}}
	root := &Agent{agentConfig: agentConfig{Name: "Root", taskQueue: "q-root", subAgents: []*Agent{mid}}}
	got := root.buildSubAgentRoutes()
	midKey := SubAgentToolName(mid)
	rMid, ok := got[midKey]
	if !ok {
		t.Fatalf("missing mid %q", midKey)
	}
	if rMid.ChildRoutes == nil {
		t.Fatal("expected nested child routes")
	}
	leafKey := SubAgentToolName(leaf)
	rLeaf, ok := rMid.ChildRoutes[leafKey]
	if !ok {
		t.Fatalf("missing leaf %q", leafKey)
	}
	if rLeaf.TaskQueue != "q-leaf" || len(rLeaf.ChildRoutes) != 0 {
		t.Fatalf("leaf route = %+v", rLeaf)
	}
}

func TestBuildWorkflowSubAgentRoutes_skipsEmptyTaskQueue(t *testing.T) {
	skip := &Agent{agentConfig: agentConfig{Name: "X", taskQueue: "  "}}
	parent := &Agent{agentConfig: agentConfig{subAgents: []*Agent{skip}}}
	if got := parent.buildSubAgentRoutes(); len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}
