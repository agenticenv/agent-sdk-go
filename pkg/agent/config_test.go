package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	mcpclient "github.com/agenticenv/agent-sdk-go/pkg/mcp/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBuildAgentConfig_NeitherTemporalConfigNorClient(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		WithLLMClient(stubLLM{}),
	})
	if err == nil || !strings.Contains(err.Error(), "temporal connection is required") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildAgentConfig_EmptyTaskQueue(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		WithTemporalConfig(&TemporalConfig{TaskQueue: ""}),
		WithLLMClient(stubLLM{}),
	})
	if err == nil || !strings.Contains(err.Error(), "TaskQueue") {
		t.Fatalf("got %v", err)
	}
}

func TestNewMCPTool(t *testing.T) {
	tool := NewMCPTool("srv", interfaces.ToolSpec{Name: "echo", Description: "d", Parameters: nil}, nil)
	if tool.Name() != "mcp_srv_echo" || tool.Description() != "d" {
		t.Fatal()
	}
	p := tool.Parameters()
	if p["type"] != "object" {
		t.Fatalf("%v", p)
	}
}

func TestValidateMCPClients(t *testing.T) {
	t.Run("duplicate_name", func(t *testing.T) {
		noop := types.MCPStdio{Command: "go", Args: []string{"version"}}
		cl1, err := mcpclient.NewClient("a", noop)
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		cl2, err := mcpclient.NewClient("a", noop)
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		err = validateMCPClients([]interfaces.MCPClient{cl1, cl2})
		if err == nil || !strings.Contains(err.Error(), "duplicate mcp client name") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nil", func(t *testing.T) {
		err := validateMCPClients([]interfaces.MCPClient{nil})
		if err == nil || !strings.Contains(err.Error(), "nil") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestBuildAgentConfig_WithMCP(t *testing.T) {
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "v0.0.1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "keep", Description: "k", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "drop", Description: "d", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	srvSess, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srvSess.Close()

	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		WithTemporalConfig(&TemporalConfig{TaskQueue: "q"}),
		WithLLMClient(stubLLM{}),
		WithMCPConfig(MCPServers{"srv": MCPConfig{
			Transport:  types.MCPLoopback{Transport: t2},
			ToolFilter: types.MCPToolFilter{AllowTools: []string{"keep"}},
		}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.mcpTools) != 1 || cfg.mcpTools[0].Name() != "mcp_srv_keep" {
		t.Fatalf("mcpTools = %v", cfg.mcpTools)
	}
}

func TestBuildAgentConfig_MCPClients_toolFilter(t *testing.T) {
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "v0.0.1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "keep", Description: "k", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "drop", Description: "d", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, map[string]any{"ok": true}, nil
	})
	srvSess, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srvSess.Close()

	cl, err := mcpclient.NewClient("s", types.MCPLoopback{Transport: t2},
		mcpclient.WithToolFilter(types.MCPToolFilter{AllowTools: []string{"keep"}}))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		WithTemporalConfig(&TemporalConfig{TaskQueue: "q"}),
		WithLLMClient(stubLLM{}),
		WithMCPClients(cl),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.mcpTools) != 1 || cfg.mcpTools[0].Name() != "mcp_s_keep" {
		t.Fatalf("mcpTools = %v", cfg.mcpTools)
	}
}

func TestBuildAgentConfig_MCP_duplicateClientName(t *testing.T) {
	cl, cerr := mcpclient.NewClient("dup", types.MCPStdio{Command: "go", Args: []string{"version"}})
	if cerr != nil {
		t.Fatalf("new client: %v", cerr)
	}
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		WithTemporalConfig(&TemporalConfig{TaskQueue: "q"}),
		WithLLMClient(stubLLM{}),
		WithMCPConfig(MCPServers{"dup": MCPConfig{
			Transport: types.MCPStdio{Command: "go", Args: []string{"env"}},
		}}),
		WithMCPClients(cl),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate mcp client name") {
		t.Fatalf("got %v", err)
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

func TestAgentConfig_buildSubAgentTools_duplicateRootSubs(t *testing.T) {
	s := &Agent{agentConfig: agentConfig{Name: "Same"}}
	c := &agentConfig{subAgents: []*Agent{s, s}, maxSubAgentDepth: 3}
	err := c.buildSubAgentTools()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestAgentConfig_buildSubAgentTools_duplicateDerivedToolName(t *testing.T) {
	a := &Agent{agentConfig: agentConfig{Name: "Dup"}}
	b := &Agent{agentConfig: agentConfig{Name: "Dup"}}
	c := &agentConfig{subAgents: []*Agent{a, b}, maxSubAgentDepth: 3}
	err := c.buildSubAgentTools()
	if err == nil || !strings.Contains(err.Error(), "duplicate sub-agent tool name") {
		t.Fatalf("want duplicate sub-agent tool name error, got %v", err)
	}
}

func TestAgentConfig_buildSubAgentTools_nilSubAgent(t *testing.T) {
	c := &agentConfig{subAgents: []*Agent{nil}, maxSubAgentDepth: 3}
	err := c.buildSubAgentTools()
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("want nil sub-agent error, got %v", err)
	}
}

func TestAgentConfig_buildSubAgentTools_invalidSubAgentName(t *testing.T) {
	emptyName := &Agent{agentConfig: agentConfig{Name: "", ID: "id-only"}}
	c := &agentConfig{subAgents: []*Agent{emptyName}, maxSubAgentDepth: 3}
	if err := c.buildSubAgentTools(); err == nil {
		t.Fatal("expected error for empty sub-agent name")
	}
	symbolsOnly := &Agent{agentConfig: agentConfig{Name: "@@@"}}
	c2 := &agentConfig{subAgents: []*Agent{symbolsOnly}, maxSubAgentDepth: 3}
	if err := c2.buildSubAgentTools(); err == nil {
		t.Fatal("expected error for sub-agent name with no alphanumeric characters")
	}
}

func TestAgentConfig_buildSubAgentTools_cycleAB(t *testing.T) {
	a := &Agent{agentConfig: agentConfig{Name: "A"}}
	b := &Agent{agentConfig: agentConfig{Name: "B"}}
	a.subAgents = []*Agent{b}
	b.subAgents = []*Agent{a}
	c := &agentConfig{subAgents: []*Agent{a}, maxSubAgentDepth: 5}
	err := c.buildSubAgentTools()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestAgentConfig_buildSubAgentTools_depthExceeded(t *testing.T) {
	d1 := &Agent{agentConfig: agentConfig{Name: "d1"}}
	d2 := &Agent{agentConfig: agentConfig{Name: "d2"}}
	d3 := &Agent{agentConfig: agentConfig{Name: "d3"}}
	d4 := &Agent{agentConfig: agentConfig{Name: "d4"}}
	d1.subAgents = []*Agent{d2}
	d2.subAgents = []*Agent{d3}
	d3.subAgents = []*Agent{d4}
	c := &agentConfig{subAgents: []*Agent{d1}, maxSubAgentDepth: 3}
	err := c.buildSubAgentTools()
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("want depth error, got %v", err)
	}
}

func TestAgentConfig_buildSubAgentTools_okWithinDepth(t *testing.T) {
	d1 := &Agent{agentConfig: agentConfig{Name: "d1"}}
	d2 := &Agent{agentConfig: agentConfig{Name: "d2"}}
	d3 := &Agent{agentConfig: agentConfig{Name: "d3"}}
	d1.subAgents = []*Agent{d2}
	d2.subAgents = []*Agent{d3}
	c := &agentConfig{subAgents: []*Agent{d1}, maxSubAgentDepth: 3}
	if err := c.buildSubAgentTools(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfig_validateToolNames_conflict(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "Math"}}
	c := &agentConfig{
		tools:     []interfaces.Tool{mockTool{name: "subagent_Math"}},
		subAgents: []*Agent{sub},
	}
	if err := c.buildSubAgentTools(); err != nil {
		t.Fatal(err)
	}
	err := c.validateToolNames()
	if err == nil || (!strings.Contains(err.Error(), "duplicate tool name") && !strings.Contains(err.Error(), "conflicts")) {
		t.Fatalf("want duplicate / conflict error, got %v", err)
	}
}

func TestAgentConfig_toolsList_includesSubAgents(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "Helper", ID: "id-sub"}}
	c := &agentConfig{
		tools:     []interfaces.Tool{mockTool{name: "echo"}},
		subAgents: []*Agent{sub},
	}
	if err := c.buildSubAgentTools(); err != nil {
		t.Fatal(err)
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
