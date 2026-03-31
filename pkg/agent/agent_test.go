package agent

import (
	"context"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func TestGetEventTaskQueue(t *testing.T) {
	if got := getEventTaskQueue("my-queue"); got != "my-queue-events" {
		t.Errorf("getEventTaskQueue(%q) = %q, want my-queue-events", "my-queue", got)
	}
}

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

func TestCopyEventWithShortApprovalToken(t *testing.T) {
	ev := &AgentEvent{Type: AgentEventApproval, Approval: &ApprovalEvent{ToolName: "t", ApprovalToken: "long"}}
	got := copyEventWithShortApprovalToken(ev, "short")
	if got == ev {
		t.Error("should return a copy")
	}
	if got.Approval.ApprovalToken != "short" {
		t.Errorf("ApprovalToken = %q, want short", got.Approval.ApprovalToken)
	}
	if got.Approval.ToolName != "t" {
		t.Errorf("ToolName = %q, want t", got.Approval.ToolName)
	}

	evNoApproval := &AgentEvent{Type: AgentEventContent}
	got2 := copyEventWithShortApprovalToken(evNoApproval, "x")
	if got2.Approval != nil {
		t.Error("ev with nil Approval should produce nil Approval in copy")
	}
}

func TestStreamCompleteEndsRun(t *testing.T) {
	root := "Main agent"
	if streamCompleteEndsRun(nil, root) {
		t.Error("nil event should not end run")
	}
	if streamCompleteEndsRun(&AgentEvent{Type: AgentEventContent}, root) {
		t.Error("non-complete should not end run")
	}
	if !streamCompleteEndsRun(&AgentEvent{Type: AgentEventComplete, AgentName: ""}, root) {
		t.Error("complete with empty agent should end run (legacy)")
	}
	if !streamCompleteEndsRun(&AgentEvent{Type: AgentEventComplete, AgentName: root}, root) {
		t.Error("complete from root should end run")
	}
	if streamCompleteEndsRun(&AgentEvent{Type: AgentEventComplete, AgentName: "MathSpecialist"}, root) {
		t.Error("complete from sub-agent should not end root run")
	}
}

func TestAgent_ValidateConversationID(t *testing.T) {
	l := logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{Level: "error"}))
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

func TestAgent_BeginRunEndRun(t *testing.T) {
	l := logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{Level: "error"}))
	a := &Agent{agentConfig: agentConfig{logger: l}}

	cleanup, err := a.beginRun("wf1")
	if err != nil {
		t.Fatalf("beginRun: %v", err)
	}
	cleanup()

	_, err = a.beginRun("wf1")
	if err != nil {
		t.Fatalf("beginRun after cleanup: %v", err)
	}
	_, err = a.beginRun("wf2")
	if err != ErrAgentAlreadyRunning {
		t.Errorf("beginRun concurrent: got %v, want ErrAgentAlreadyRunning", err)
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

func TestSubAgentQueryFromArgs(t *testing.T) {
	if subAgentQueryFromArgs(nil) != "" {
		t.Error("nil args")
	}
	if subAgentQueryFromArgs(map[string]any{}) != "" {
		t.Error("empty map")
	}
	if got := subAgentQueryFromArgs(map[string]any{"query": "hello"}); got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestSubAgentChildWorkflowTimeout(t *testing.T) {
	if got := subAgentChildWorkflowTimeout(nil); got != defaultTimeout {
		t.Fatalf("nil worker: got %v want %v", got, defaultTimeout)
	}
	aw := &AgentWorker{config: &agentConfig{timeout: 2 * time.Minute}}
	if got := subAgentChildWorkflowTimeout(aw); got != 2*time.Minute {
		t.Fatalf("custom timeout: got %v", got)
	}
	aw.config.timeout = 0
	if got := subAgentChildWorkflowTimeout(aw); got != defaultTimeout {
		t.Fatalf("zero timeout: got %v want %v", got, defaultTimeout)
	}
}

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
