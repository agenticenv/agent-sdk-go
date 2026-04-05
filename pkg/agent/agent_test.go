package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang/mock/gomock"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	rtmocks "github.com/agenticenv/agent-sdk-go/internal/runtime/mocks"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func testAgentWithRuntime(rt runtime.Runtime) *Agent {
	return &Agent{
		agentConfig: agentConfig{
			Name:             "TestAgent",
			logger:           logger.DefaultLogger("error"),
			maxSubAgentDepth: 2,
		},
		runtime: rt,
	}
}

func TestAgent_Run_ForwardsRequestAndReturnsResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, req *runtime.ExecuteRequest) (*types.AgentResponse, error) {
		if req.StreamingEnabled {
			t.Error("Run must set StreamingEnabled false")
		}
		if req.UserPrompt != "hello" {
			t.Errorf("UserPrompt = %q", req.UserPrompt)
		}
		name := ""
		if req.AgentSpec != nil {
			name = req.AgentSpec.Name
		}
		return &types.AgentResponse{Content: "reply", AgentName: name, Model: "m1"}, nil
	})

	a := testAgentWithRuntime(mockRT)
	resp, err := a.Run(context.Background(), "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "reply" || resp.Model != "m1" || resp.AgentName != "TestAgent" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestAgent_Stream_SetsStreamingEnabled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	var streamReq *runtime.ExecuteRequest
	mockRT.EXPECT().ExecuteStream(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, req *runtime.ExecuteRequest) (chan *types.AgentEvent, error) {
		streamReq = req
		ch := make(chan *types.AgentEvent, 2)
		evName := ""
		if req.AgentSpec != nil {
			evName = req.AgentSpec.Name
		}
		ch <- &types.AgentEvent{Type: types.AgentEventComplete, AgentName: evName, Content: "done"}
		close(ch)
		return ch, nil
	})

	a := testAgentWithRuntime(mockRT)
	ch, err := a.Stream(context.Background(), "prompt", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for range ch {
		}
	}()
	if streamReq == nil || !streamReq.StreamingEnabled {
		t.Fatalf("Stream request = %+v", streamReq)
	}
	if streamReq.UserPrompt != "prompt" {
		t.Errorf("UserPrompt = %q", streamReq.UserPrompt)
	}
	ev := <-ch
	if ev == nil || ev.Type != types.AgentEventComplete {
		t.Fatalf("event = %+v", ev)
	}
}

func TestAgent_RunAsync_DeliversResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(&types.AgentResponse{Content: "mock", AgentName: "TestAgent", Model: "stub"}, nil)

	a := testAgentWithRuntime(mockRT)
	resCh, apprCh, err := a.RunAsync(context.Background(), "async", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-resCh:
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if r.Response == nil || r.Response.Content != "mock" {
			t.Fatalf("result = %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for RunAsync result")
	}
	for range apprCh {
		t.Fatal("unexpected approval request")
	}
}

func TestAgent_Stream_CustomStreamFn(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().ExecuteStream(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, req *runtime.ExecuteRequest) (chan *types.AgentEvent, error) {
		ch := make(chan *types.AgentEvent, 1)
		ch <- &types.AgentEvent{Type: types.AgentEventContent, Content: "partial"}
		close(ch)
		return ch, nil
	})

	a := testAgentWithRuntime(mockRT)
	ch, err := a.Stream(context.Background(), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	ev := <-ch
	if ev == nil || ev.Content != "partial" {
		t.Fatalf("ev = %+v", ev)
	}
}

// stubLLM is a minimal [interfaces.LLMClient] for config/runtime unit tests.
type stubLLM struct{}

func (stubLLM) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{}, nil
}
func (stubLLM) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stub")
}
func (stubLLM) GetModel() string                           { return "stub" }
func (stubLLM) GetProvider() interfaces.LLMProvider        { return interfaces.LLMProviderOpenAI }
func (stubLLM) IsStreamSupported() bool                    { return false }

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
