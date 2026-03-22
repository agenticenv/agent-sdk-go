package agent

import (
	"context"
	"testing"

	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/logger"
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
	ev := &AgentEvent{Type: AgentEventToolApproval, Approval: &ToolApprovalEvent{ToolName: "t", ApprovalToken: "long"}}
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
func (m *mockConversation) IsDistributed() bool                          { return false }
