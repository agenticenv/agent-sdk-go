package agent

import (
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestBuildAgentConfig_NeitherTemporalConfigNorClient(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithLLMClient(nil),
	})
	if err == nil {
		t.Fatal("expected error when neither Temporal config nor client is set")
	}
}

func TestBuildAgentConfig_EmptyTaskQueue(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithTemporalConfig(&TemporalConfig{TaskQueue: ""}),
		WithLLMClient(nil),
	})
	if err == nil {
		t.Fatal("expected error when TaskQueue is empty")
	}
}

func TestAgentConfig_ToolsList(t *testing.T) {
	tool := mockTool{name: "t1"}
	c := &agentConfig{tools: []interfaces.Tool{tool}}
	list := c.toolsList()
	if len(list) != 1 || list[0].Name() != "t1" {
		t.Errorf("toolsList = %v, want [t1]", list)
	}

	reg := &mockRegistry{tools: []interfaces.Tool{tool, mockTool{name: "t2"}}}
	c2 := &agentConfig{toolRegistry: reg}
	list2 := c2.toolsList()
	if len(list2) != 2 {
		t.Errorf("toolsList with registry = %v, want 2 tools", list2)
	}
}

func TestAgentConfig_ResponseFormatForLLM(t *testing.T) {
	c := &agentConfig{}
	rf := c.responseFormatForLLM()
	if rf.Type != interfaces.ResponseFormatText {
		t.Errorf("default responseFormat = %v, want text", rf.Type)
	}

	c.responseFormat = &interfaces.ResponseFormat{Type: interfaces.ResponseFormatJSON}
	rf = c.responseFormatForLLM()
	if rf.Type != interfaces.ResponseFormatJSON {
		t.Errorf("with override = %v, want json", rf.Type)
	}
}

func TestAgentConfig_ApplySamplingToRequest(t *testing.T) {
	req := &interfaces.LLMRequest{}
	c := &agentConfig{}
	c.applySamplingToRequest(req)
	if req.Temperature != nil || req.MaxTokens != 0 {
		t.Error("nil llmSampling should not modify request")
	}

	temp := 0.5
	c.llmSampling = &LLMSampling{Temperature: &temp, MaxTokens: 100}
	c.applySamplingToRequest(req)
	if req.Temperature == nil || *req.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", req.Temperature)
	}
	if req.MaxTokens != 100 {
		t.Errorf("MaxTokens = %d, want 100", req.MaxTokens)
	}
}

func TestAgentConfig_RequiresApproval(t *testing.T) {
	approvalTool := mockToolWithApproval{mockTool: mockTool{name: "a"}, needApproval: true}
	noApprovalTool := mockToolWithApproval{mockTool: mockTool{name: "b"}, needApproval: false}

	// No policy: use tool's ApprovalRequired
	c := &agentConfig{}
	if !c.requiresApproval(approvalTool) {
		t.Error("requiresApproval with no policy: approval tool should require approval")
	}
	if c.requiresApproval(noApprovalTool) {
		t.Error("requiresApproval with no policy: non-approval tool should not require approval")
	}

	// With RequireAllToolApprovalPolicy
	c.toolApprovalPolicy = RequireAllToolApprovalPolicy{}
	if !c.requiresApproval(noApprovalTool) {
		t.Error("RequireAllToolApprovalPolicy: all tools should require approval")
	}

	// With AutoToolApprovalPolicy
	c.toolApprovalPolicy = AutoToolApprovalPolicy()
	if c.requiresApproval(approvalTool) {
		t.Error("AutoToolApprovalPolicy: no tool should require approval")
	}
}

func TestAgentConfig_validateSubAgents_duplicateRootSubs(t *testing.T) {
	s := &Agent{agentConfig: agentConfig{Name: "Same"}}
	c := &agentConfig{subAgents: []*Agent{s, s}, maxSubAgentDepth: 3}
	err := c.validateSubAgents()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestAgentConfig_validateSubAgents_nilSubAgent(t *testing.T) {
	c := &agentConfig{subAgents: []*Agent{nil}, maxSubAgentDepth: 3}
	err := c.validateSubAgents()
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("want nil sub-agent error, got %v", err)
	}
}

func TestAgentConfig_validateSubAgents_cycleAB(t *testing.T) {
	a := &Agent{agentConfig: agentConfig{Name: "A"}}
	b := &Agent{agentConfig: agentConfig{Name: "B"}}
	a.subAgents = []*Agent{b}
	b.subAgents = []*Agent{a}
	c := &agentConfig{subAgents: []*Agent{a}, maxSubAgentDepth: 5}
	err := c.validateSubAgents()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestAgentConfig_validateSubAgents_depthExceeded(t *testing.T) {
	d1 := &Agent{agentConfig: agentConfig{Name: "d1"}}
	d2 := &Agent{agentConfig: agentConfig{Name: "d2"}}
	d3 := &Agent{agentConfig: agentConfig{Name: "d3"}}
	d4 := &Agent{agentConfig: agentConfig{Name: "d4"}}
	d1.subAgents = []*Agent{d2}
	d2.subAgents = []*Agent{d3}
	d3.subAgents = []*Agent{d4}
	c := &agentConfig{subAgents: []*Agent{d1}, maxSubAgentDepth: 3}
	err := c.validateSubAgents()
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("want depth error, got %v", err)
	}
}

func TestAgentConfig_validateSubAgents_okWithinDepth(t *testing.T) {
	d1 := &Agent{agentConfig: agentConfig{Name: "d1"}}
	d2 := &Agent{agentConfig: agentConfig{Name: "d2"}}
	d3 := &Agent{agentConfig: agentConfig{Name: "d3"}}
	d1.subAgents = []*Agent{d2}
	d2.subAgents = []*Agent{d3}
	c := &agentConfig{subAgents: []*Agent{d1}, maxSubAgentDepth: 3}
	if err := c.validateSubAgents(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfig_validateToolsAndSubAgentNames_conflict(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "Math"}}
	c := &agentConfig{
		tools:     []interfaces.Tool{mockTool{name: "subagent_Math"}},
		subAgents: []*Agent{sub},
	}
	err := c.validateToolsAndSubAgentNames()
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("want conflict error, got %v", err)
	}
}

func TestAgentConfig_toolsList_includesSubAgents(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "Helper", ID: "id-sub"}}
	c := &agentConfig{
		tools:     []interfaces.Tool{mockTool{name: "echo"}},
		subAgents: []*Agent{sub},
	}
	list := c.toolsList()
	if len(list) != 2 {
		t.Fatalf("toolsList len = %d, want 2", len(list))
	}
	if list[0].Name() != "echo" {
		t.Errorf("first tool = %s", list[0].Name())
	}
	if list[1].Name() != "subagent_Helper" {
		t.Errorf("sub tool name = %s", list[1].Name())
	}
	at, ok := list[1].(AgentTool)
	if !ok || at.SubAgent() != sub {
		t.Errorf("second tool should be AgentTool wrapping sub")
	}
}

func TestAgentConfig_HasApprovalTools(t *testing.T) {
	c := &agentConfig{
		tools:              []interfaces.Tool{mockToolWithApproval{mockTool: mockTool{name: "x"}, needApproval: true}},
		toolApprovalPolicy: RequireAllToolApprovalPolicy{},
	}
	if !c.hasApprovalTools() {
		t.Error("hasApprovalTools should be true when tools require approval")
	}

	c2 := &agentConfig{
		tools:              []interfaces.Tool{mockToolWithApproval{mockTool: mockTool{name: "x"}, needApproval: false}},
		toolApprovalPolicy: AutoToolApprovalPolicy(),
	}
	if c2.hasApprovalTools() {
		t.Error("hasApprovalTools should be false when no tool requires approval")
	}
}

type mockRegistry struct {
	tools []interfaces.Tool
}

func (m *mockRegistry) Register(interfaces.Tool) {}
func (m *mockRegistry) Get(name string) (interfaces.Tool, bool) {
	for _, t := range m.tools {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}
func (m *mockRegistry) Tools() []interfaces.Tool { return m.tools }

type mockToolWithApproval struct {
	mockTool
	needApproval bool
}

func (m mockToolWithApproval) ApprovalRequired() bool { return m.needApproval }

// Ensure mockToolWithApproval implements both interfaces
var _ interfaces.Tool = (*mockToolWithApproval)(nil)
var _ interfaces.ToolApproval = (*mockToolWithApproval)(nil)
