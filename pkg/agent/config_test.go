package agent

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/temporal"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	mcpclient "github.com/agenticenv/agent-sdk-go/pkg/mcp/client"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// agentConfigFingerprint is a test helper for Temporal per-run fingerprint payloads.
func agentConfigFingerprint(c *agentConfig) string {
	tools, err := c.resolveTools(context.Background())
	if err != nil {
		panic(err)
	}
	return agentConfigFingerprintTools(c, tools)
}

func agentConfigFingerprintTools(c *agentConfig, tools []interfaces.Tool) string {
	convSize := 0
	if c.conversationConfig != nil {
		convSize = c.conversationConfig.Size
	}
	return temporal.ComputeAgentFingerprint(temporal.BuildAgentFingerprintPayload(
		c.runtimeAgentSpec(),
		temporal.ToolNamesFromTools(tools),
		toolPolicyFingerprint(c.toolApprovalPolicy),
		llmSamplingRuntimeView(c.llmSampling),
		convSize,
		runtime.AgentLimits{
			MaxIterations:   c.maxIterations,
			Timeout:         c.timeout,
			ApprovalTimeout: c.approvalTimeout,
			Budget:          c.budgetConfig,
		},
		mcpConfigFingerprint(c.mcpServers, mcpExtraClientNames(c.mcpClients)),
		a2aConfigFingerprint(c.a2aServers, a2aExtraClientNames(c.a2aClients)),
		observabilityConfigFingerprint(c.observabilityConfig),
		string(c.agentMode),
		c.agentToolExecutionMode,
		retrieverConfigFingerprint(c.retrieverMode, c.retrievers),
		hookGroupsFingerprint(c.hooks),
	))
}

func TestBuildAgentConfig_NeitherTemporalConfigNorClient_UsesLocalRuntime(t *testing.T) {
	// No Temporal config is valid — the local runtime is the default backend.
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		WithLLMClient(testLLM(t)),
	})
	if err != nil {
		t.Fatalf("expected success with local backend, got: %v", err)
	}
	if cfg.hasTemporalRuntime() {
		t.Fatal("expected local backend (hasTemporalRuntime should be false)")
	}
}

func TestSanitizeName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"Math Assistant", "math-assistant"},
		{"  Foo_Bar  ", "foo_bar"},
		{"A@B#C", "abc"},
		{"", ""},
		{strings.Repeat("a", 80), strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		if got := sanitizeName(tc.in); got != tc.want {
			t.Fatalf("sanitizeName(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildAgentConfig_NameSanitizeAndValidate(t *testing.T) {
	t.Parallel()
	cfg, err := buildAgentConfig([]Option{
		WithName("Math Assistant"),
		WithLLMClient(testLLM(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "math-assistant" {
		t.Fatalf("got %q", cfg.Name)
	}
}

func TestBuildAgentConfig_InstanceIdDeprecatedIgnored(t *testing.T) {
	t.Parallel()
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		WithInstanceId("agent-1_pod"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.instanceId != "agent-1_pod" {
		t.Fatalf("got %q", cfg.instanceId)
	}
}

func TestValidateSubAgentRegistry_RootNameCollision(t *testing.T) {
	t.Parallel()
	sub := &Agent{agentConfig: agentConfig{Name: "same"}}
	c := &agentConfig{Name: "same", subAgents: []*Agent{sub}, maxSubAgentDepth: 3}
	err := c.buildSubAgentRegistry()
	if err == nil || !strings.Contains(err.Error(), "must differ from root agent name") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildAgentConfig_DefaultNoopTracerMetrics(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("noop-obs"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.tracer.(*observability.NoopTracer); !ok {
		t.Fatalf("without observability wiring, tracer should be *observability.NoopTracer, got %T", cfg.tracer)
	}
	if _, ok := cfg.metrics.(*observability.NoopMetrics); !ok {
		t.Fatalf("without observability wiring, metrics should be *observability.NoopMetrics, got %T", cfg.metrics)
	}
	if _, ok := cfg.logs.(*observability.NoopLogs); !ok {
		t.Fatalf("without observability wiring, logs should be *observability.NoopLogs, got %T", cfg.logs)
	}
}

func TestBuildAgentConfig_EmptyTaskQueue(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal(""),
		WithLLMClient(testLLM(t)),
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
	defer func() { _ = srvSess.Close() }()

	_, err = buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMCPConfig(MCPServers{"srv": MCPConfig{
			Transport:  types.MCPLoopback{Transport: t2},
			ToolFilter: types.MCPToolFilter{AllowTools: []string{"keep"}},
		}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// buildAgentConfig calls resolveTools; success means MCP discovery + filter produced valid tools.
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
	defer func() { _ = srvSess.Close() }()

	cl, err := mcpclient.NewClient("s", types.MCPLoopback{Transport: t2},
		mcpclient.WithToolFilter(types.MCPToolFilter{AllowTools: []string{"keep"}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMCPClients(cl),
	})
	if err != nil {
		t.Fatal(err)
	}
	// buildAgentConfig calls resolveTools; success means MCP discovery + filter produced valid tools.
}

func TestBuildAgentConfig_MCP_duplicateClientName(t *testing.T) {
	cl, cerr := mcpclient.NewClient("dup", types.MCPStdio{Command: "go", Args: []string{"version"}})
	if cerr != nil {
		t.Fatalf("new client: %v", cerr)
	}
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMCPConfig(MCPServers{"dup": MCPConfig{
			Transport: types.MCPStdio{Command: "go", Args: []string{"env"}},
		}}),
		WithMCPClients(cl),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate mcp client name") && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
}

func TestAgentConfig_ToolsList(t *testing.T) {
	tool := testTool(t, "t1")
	c := &agentConfig{tools: []interfaces.Tool{tool}}
	if err := c.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	list, err := c.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name() != "t1" {
		t.Errorf("toolsList = %v, want [t1]", list)
	}

	reg := NewToolRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(testTool(t, "t2")); err != nil {
		t.Fatal(err)
	}
	c2 := &agentConfig{toolRegistry: reg}
	list2, err := c2.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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
	approvalTool := testToolWithApproval(t, "a", true)
	noApprovalTool := testToolWithApproval(t, "b", false)

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

func TestAgentConfig_resolveSubAgentTools_duplicateRootSubs(t *testing.T) {
	s := &Agent{agentConfig: agentConfig{Name: "Same"}}
	c := &agentConfig{subAgents: []*Agent{s, s}, maxSubAgentDepth: 3}
	err := c.buildSubAgentRegistry()
	if err == nil || (!strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "already exists")) {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestAgentConfig_resolveSubAgentTools_duplicateDerivedToolName(t *testing.T) {
	a := &Agent{agentConfig: agentConfig{Name: "Dup"}}
	b := &Agent{agentConfig: agentConfig{Name: "Dup"}}
	c := &agentConfig{subAgents: []*Agent{a, b}, maxSubAgentDepth: 3}
	err := c.buildSubAgentRegistry()
	if err == nil || (!strings.Contains(err.Error(), "duplicate sub-agent tool name") && !strings.Contains(err.Error(), "already exists")) {
		t.Fatalf("want duplicate sub-agent tool name error, got %v", err)
	}
}

func TestAgentConfig_resolveSubAgentTools_nilSubAgent(t *testing.T) {
	c := &agentConfig{subAgents: []*Agent{nil}, maxSubAgentDepth: 3}
	err := c.buildSubAgentRegistry()
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("want nil sub-agent error, got %v", err)
	}
}

func TestAgentConfig_resolveSubAgentTools_invalidSubAgentName(t *testing.T) {
	emptyName := &Agent{agentConfig: agentConfig{Name: "", ID: "id-only"}}
	c := &agentConfig{subAgents: []*Agent{emptyName}, maxSubAgentDepth: 3}
	if err := c.buildSubAgentRegistry(); err == nil {
		t.Fatal("expected error for empty sub-agent name")
	}
	symbolsOnly := &Agent{agentConfig: agentConfig{Name: "@@@"}}
	c2 := &agentConfig{subAgents: []*Agent{symbolsOnly}, maxSubAgentDepth: 3}
	if err := c2.buildSubAgentRegistry(); err == nil {
		t.Fatal("expected error for sub-agent name with no alphanumeric characters")
	}
}

func TestAgentConfig_resolveSubAgentTools_cycleAB(t *testing.T) {
	a := &Agent{agentConfig: agentConfig{Name: "A", subAgentRegistry: NewSubAgentRegistry()}}
	b := &Agent{agentConfig: agentConfig{Name: "B", subAgentRegistry: NewSubAgentRegistry()}}
	_ = a.subAgentRegistry.Register(b)
	_ = b.subAgentRegistry.Register(a)
	c := &agentConfig{subAgents: []*Agent{a}, maxSubAgentDepth: 5}
	err := c.buildSubAgentRegistry()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestAgentConfig_resolveSubAgentTools_depthExceeded(t *testing.T) {
	d4 := &Agent{agentConfig: agentConfig{Name: "d4", subAgentRegistry: NewSubAgentRegistry()}}
	d3 := &Agent{agentConfig: agentConfig{Name: "d3", subAgentRegistry: NewSubAgentRegistry()}}
	d2 := &Agent{agentConfig: agentConfig{Name: "d2", subAgentRegistry: NewSubAgentRegistry()}}
	d1 := &Agent{agentConfig: agentConfig{Name: "d1", subAgentRegistry: NewSubAgentRegistry()}}
	_ = d3.subAgentRegistry.Register(d4)
	_ = d2.subAgentRegistry.Register(d3)
	_ = d1.subAgentRegistry.Register(d2)
	c := &agentConfig{subAgents: []*Agent{d1}, maxSubAgentDepth: 3}
	err := c.buildSubAgentRegistry()
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("want depth error, got %v", err)
	}
}

func TestAgentConfig_resolveSubAgentTools_okWithinDepth(t *testing.T) {
	d3 := &Agent{agentConfig: agentConfig{Name: "d3", subAgentRegistry: NewSubAgentRegistry()}}
	d2 := &Agent{agentConfig: agentConfig{Name: "d2", subAgentRegistry: NewSubAgentRegistry()}}
	d1 := &Agent{agentConfig: agentConfig{Name: "d1", subAgentRegistry: NewSubAgentRegistry()}}
	_ = d2.subAgentRegistry.Register(d3)
	_ = d1.subAgentRegistry.Register(d2)
	c := &agentConfig{subAgents: []*Agent{d1}, maxSubAgentDepth: 3}
	if err := c.buildSubAgentRegistry(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfig_validateToolNames_conflict(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "Math"}}
	c := &agentConfig{
		tools:     []interfaces.Tool{testTool(t, "subagent_Math")},
		subAgents: []*Agent{sub},
	}
	if err := c.buildRegistries(); err != nil {
		t.Fatal(err)
	}
	subs, err := c.resolveSubAgentTools()
	if err != nil {
		t.Fatal(err)
	}
	tools := append(c.toolRegistry.List(), subs...)
	err = validateToolNames(tools)
	if err == nil || (!strings.Contains(err.Error(), "duplicate tool name") && !strings.Contains(err.Error(), "conflicts")) {
		t.Fatalf("want duplicate / conflict error, got %v", err)
	}
}

func TestAgentConfig_toolsList_includesSubAgents(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "Helper", ID: "id-sub"}}
	c := &agentConfig{
		tools:     []interfaces.Tool{testTool(t, "echo")},
		subAgents: []*Agent{sub},
	}
	if err := c.buildRegistries(); err != nil {
		t.Fatal(err)
	}
	list, err := c.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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
		tools:              []interfaces.Tool{testToolWithApproval(t, "x", true)},
		toolApprovalPolicy: RequireAllToolApprovalPolicy{},
	}
	if err := c.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	if !c.hasApprovalTools(c.toolRegistry.List()) {
		t.Error("hasApprovalTools should be true when tools require approval")
	}

	c2 := &agentConfig{
		tools:              []interfaces.Tool{testToolWithApproval(t, "x", false)},
		toolApprovalPolicy: AutoToolApprovalPolicy(),
	}
	if err := c2.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	if c2.hasApprovalTools(c2.toolRegistry.List()) {
		t.Error("hasApprovalTools should be false when no tool requires approval")
	}
}

func TestBuildAgentConfig_approvalTimeoutValidatedWithoutApprovalTools(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTimeout(5 * time.Minute),
		WithApprovalTimeout(6 * time.Minute),
	})
	if err == nil || !strings.Contains(err.Error(), "approvalTimeout") {
		t.Fatalf("got %v", err)
	}

	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTimeout(5 * time.Minute),
		WithApprovalTimeout(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.approvalTimeout != 2*time.Minute {
		t.Fatalf("approvalTimeout = %v", cfg.approvalTimeout)
	}
}

func TestBuildAgentConfig_executionConfigsMappedToRuntime(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("exec"),
		WithLLMClient(testLLM(t)),
		WithLLMExecutionConfig(ExecutionConfig{Timeout: 45 * time.Minute, MaxAttempts: 4}),
		WithToolAuthExecutionConfig(ExecutionConfig{MaxAttempts: 2}),
		WithToolExecutionConfig(ExecutionConfig{Timeout: 25 * time.Minute}),
		WithMCPExecutionConfig(ExecutionConfig{Timeout: 20 * time.Minute, MaxAttempts: 2}),
		WithA2AExecutionConfig(ExecutionConfig{MaxAttempts: 5}),
		WithRetrieverExecutionConfig(ExecutionConfig{Timeout: 3 * time.Minute}),
		WithMemoryExecutionConfig(ExecutionConfig{MaxAttempts: 4}),
		WithConversationExecutionConfig(ExecutionConfig{Timeout: 45 * time.Second}),
		WithSubAgentExecutionConfig(ExecutionConfig{MaxAttempts: 2}),
	})
	if err != nil {
		t.Fatal(err)
	}

	rtCfg := cfg.runtimeAgentConfig()
	if rtCfg.ExecutionConfigs.LLM != (ExecutionConfig{Timeout: 45 * time.Minute, MaxAttempts: 4}) {
		t.Fatalf("LLM exec = %+v", rtCfg.ExecutionConfigs.LLM)
	}
	if rtCfg.ExecutionConfigs.ToolAuth != (ExecutionConfig{MaxAttempts: 2}) {
		t.Fatalf("ToolAuth exec = %+v", rtCfg.ExecutionConfigs.ToolAuth)
	}
	if rtCfg.ExecutionConfigs.ToolExecute != (ExecutionConfig{Timeout: 25 * time.Minute}) {
		t.Fatalf("ToolExecute exec = %+v", rtCfg.ExecutionConfigs.ToolExecute)
	}
	if rtCfg.ExecutionConfigs.MCP != (ExecutionConfig{Timeout: 20 * time.Minute, MaxAttempts: 2}) {
		t.Fatalf("MCP exec = %+v", rtCfg.ExecutionConfigs.MCP)
	}
	if rtCfg.ExecutionConfigs.A2A != (ExecutionConfig{MaxAttempts: 5}) {
		t.Fatalf("A2A exec = %+v", rtCfg.ExecutionConfigs.A2A)
	}
	if rtCfg.ExecutionConfigs.Retriever != (ExecutionConfig{Timeout: 3 * time.Minute}) {
		t.Fatalf("Retriever exec = %+v", rtCfg.ExecutionConfigs.Retriever)
	}
	if rtCfg.ExecutionConfigs.Memory != (ExecutionConfig{MaxAttempts: 4}) {
		t.Fatalf("Memory exec = %+v", rtCfg.ExecutionConfigs.Memory)
	}
	if rtCfg.ExecutionConfigs.Conversation != (ExecutionConfig{Timeout: 45 * time.Second}) {
		t.Fatalf("Conversation exec = %+v", rtCfg.ExecutionConfigs.Conversation)
	}
	if rtCfg.ExecutionConfigs.SubAgent != (ExecutionConfig{MaxAttempts: 2}) {
		t.Fatalf("SubAgent exec = %+v", rtCfg.ExecutionConfigs.SubAgent)
	}

	resolved := runtime.ResolveExecutionPolicies(rtCfg.ExecutionConfigs)
	if resolved.LLM.Timeout != 45*time.Minute || resolved.LLM.MaxAttempts != 4 {
		t.Fatalf("resolved LLM = %+v", resolved.LLM)
	}
	if resolved.ToolAuth.Timeout != 30*time.Minute || resolved.ToolAuth.MaxAttempts != 2 {
		t.Fatalf("resolved ToolAuth = %+v", resolved.ToolAuth)
	}
	if resolved.SubAgent.MaxAttempts != 2 {
		t.Fatalf("resolved SubAgent = %+v", resolved.SubAgent)
	}
}

// ---------------------------------------------------------------------------
// A2A test helpers
// ---------------------------------------------------------------------------

// newTestA2ACardServer starts an httptest server that serves a minimal agent card at the
// well-known path. It registers t.Cleanup to stop the server automatically.
func newTestA2ACardServer(t *testing.T, skills []a2a.AgentSkill) string {
	t.Helper()
	card := &a2a.AgentCard{
		Name:    "Test Agent",
		Version: "1.0",
		Skills:  skills,
	}
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// ---------------------------------------------------------------------------
// Retriever config tests
// ---------------------------------------------------------------------------

func TestValidateRetrieverMode(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		mode, err := validateRetrieverMode("")
		if err != nil || mode != RetrieverModeAgentic {
			t.Fatalf("mode=%q err=%v", mode, err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		for _, want := range []RetrieverMode{
			RetrieverModeAgentic,
			RetrieverModePrefetch,
			RetrieverModeHybrid,
		} {
			mode, err := validateRetrieverMode(want)
			if err != nil || mode != want {
				t.Fatalf("want %q got %q err=%v", want, mode, err)
			}
		}
	})
	t.Run("invalid", func(t *testing.T) {
		_, err := validateRetrieverMode(RetrieverMode("bogus"))
		if err == nil || !strings.Contains(err.Error(), "invalid retriever mode") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestValidateRetrievers(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		err := validateRetrievers([]interfaces.Retriever{nil})
		if err == nil || !strings.Contains(err.Error(), "nil") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("ok", func(t *testing.T) {
		if err := validateRetrievers([]interfaces.Retriever{testRetriever(t, "stub"), testRetriever(t, "stub")}); err != nil {
			t.Fatalf("got %v", err)
		}
	})
}

func TestBuildRetrieverTools(t *testing.T) {
	t.Run("agentic_builds_tools", func(t *testing.T) {
		c := &agentConfig{
			retrieverMode: RetrieverModeAgentic,
			retrievers:    []interfaces.Retriever{testRetriever(t, "kb")},
		}
		tools, err := c.resolveRetrieverTools()
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 1 || tools[0].Name() != "retriever_kb" {
			t.Fatalf("retrieverTools = %v", tools)
		}
	})
	t.Run("hybrid_builds_tools", func(t *testing.T) {
		c := &agentConfig{
			retrieverMode: RetrieverModeHybrid,
			retrievers:    []interfaces.Retriever{testRetriever(t, "stub")},
		}
		tools, err := c.resolveRetrieverTools()
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 1 {
			t.Fatalf("len = %d", len(tools))
		}
	})
	t.Run("prefetch_skips_tools", func(t *testing.T) {
		c := &agentConfig{
			retrieverMode: RetrieverModePrefetch,
			retrievers:    []interfaces.Retriever{testRetriever(t, "stub")},
		}
		tools, err := c.resolveRetrieverTools()
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 0 {
			t.Fatalf("retrieverTools = %v, want none", tools)
		}
	})
	t.Run("no_retrievers", func(t *testing.T) {
		c := &agentConfig{retrieverMode: RetrieverModeAgentic}
		tools, err := c.resolveRetrieverTools()
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 0 {
			t.Fatalf("retrieverTools = %v, want none", tools)
		}
	})
	t.Run("duplicate_name", func(t *testing.T) {
		c := &agentConfig{
			retrieverMode: RetrieverModeAgentic,
			retrievers:    []interfaces.Retriever{testRetriever(t, "x"), testRetriever(t, "x")},
		}
		_, err := c.resolveRetrieverTools()
		if err == nil || !strings.Contains(err.Error(), "duplicate retriever name") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty_name", func(t *testing.T) {
		c := &agentConfig{
			retrieverMode: RetrieverModeAgentic,
			retrievers:    []interfaces.Retriever{testRetriever(t, "  ")},
		}
		_, err := c.resolveRetrieverTools()
		if err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestResolveMemoryTools(t *testing.T) {
	stub := testMemory(t)
	t.Run("ondemand", func(t *testing.T) {
		cfg := memory.DefaultConfig(stub)
		c := &agentConfig{memoryConfig: &cfg}
		tools, err := c.resolveMemoryTools()
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 1 || tools[0].Name() != types.SaveMemoryToolName {
			t.Fatalf("tools = %v", tools)
		}
	})
	t.Run("always", func(t *testing.T) {
		cfg := memory.DefaultConfig(stub)
		cfg.Store.Mode = memory.StoreModeAlways
		c := &agentConfig{memoryConfig: &cfg}
		tools, err := c.resolveMemoryTools()
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 0 {
			t.Fatalf("tools = %v", tools)
		}
	})
	t.Run("no_memory", func(t *testing.T) {
		c := &agentConfig{}
		tools, err := c.resolveMemoryTools()
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 0 {
			t.Fatalf("tools = %v", tools)
		}
	})
}

func TestBuildAgentConfig_WithMemory_registersSaveMemory(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMemory(memory.DefaultConfig(testMemory(t))),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		if tool.Name() == types.SaveMemoryToolName {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("save_memory not in resolved tools")
	}
}

func TestBuildAgentConfig_WithMemoryAlways_leavesExtractNil(t *testing.T) {
	cfg := memory.DefaultConfig(testMemory(t))
	cfg.Store.Mode = memory.StoreModeAlways
	got, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMemory(cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := got.runtimeAgentMemory()
	if mem.Config == nil || mem.Config.Store.Extract != nil {
		t.Fatal("expected nil Extract on config; default resolves lazily at run-end")
	}
}

func TestBuildAgentConfig_WithMemoryOnDemand_noExtract(t *testing.T) {
	cfg := memory.DefaultConfig(testMemory(t))
	got, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMemory(cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := got.runtimeAgentMemory()
	if mem.Config == nil || mem.Config.Store.Extract != nil {
		t.Fatal("expected nil Extract for ondemand")
	}
}

func TestBuildAgentConfig_WithMemoryAlways_preservesCustomExtract(t *testing.T) {
	custom := memory.ExtractFunc(func(context.Context, []interfaces.Message) ([]interfaces.MemoryRecord, error) {
		return nil, nil
	})
	cfg := memory.DefaultConfig(testMemory(t))
	cfg.Store.Mode = memory.StoreModeAlways
	cfg.Store.Extract = custom
	got, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMemory(cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := got.runtimeAgentMemory()
	if mem.Config == nil || mem.Config.Store.Extract == nil {
		t.Fatal("expected custom extract")
	}
	records, err := mem.Config.Store.Extract(context.Background(), nil)
	if err != nil || records != nil {
		t.Fatalf("custom extract: records=%v err=%v", records, err)
	}
}

func TestBuildAgentConfig_WithRetrievers(t *testing.T) {
	r1, r2 := testRetriever(t, "kb-a"), testRetriever(t, "kb-b")
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrievers(r1, r2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.retrievers) != 2 {
		t.Fatalf("retrievers len = %d", len(cfg.retrievers))
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("resolved tools len = %d, want 2 (default agentic mode)", len(tools))
	}
}

func TestBuildAgentConfig_RetrieverMode_prefetchNoTools(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrievers(testRetriever(t, "stub")),
		WithRetrieverMode(RetrieverModePrefetch),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool != nil && strings.HasPrefix(tool.Name(), "retriever_") {
			t.Fatalf("prefetch mode should not expose retriever tools, got %q", tool.Name())
		}
	}
}

func TestBuildAgentConfig_RetrieverMode_agenticBuildsTools(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrievers(testRetriever(t, "stub")),
		WithRetrieverMode(RetrieverModeAgentic),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "retriever_stub" {
		t.Fatalf("resolved tools = %v", toolNames(tools))
	}
}

func toolNames(tools []interfaces.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != nil {
			out = append(out, t.Name())
		}
	}
	return out
}

func TestBuildAgentConfig_AgenticNoRetrievers_NoTools(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrieverMode(RetrieverModeAgentic),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("resolved tools = %v, want none", toolNames(tools))
	}
}

func TestBuildAgentConfig_RetrieverMode_hybridBuildsTools(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrievers(testRetriever(t, "stub")),
		WithRetrieverMode(RetrieverModeHybrid),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "retriever_stub" {
		t.Fatalf("resolved tools = %v", toolNames(tools))
	}
}

func TestBuildAgentConfig_RetrieverDuplicateName(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrievers(testRetriever(t, "dup"), testRetriever(t, "dup")),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate retriever name") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildAgentConfig_toolsList_includesRetrieverTools(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTools(testTool(t, "echo")),
		WithRetrievers(testRetriever(t, "stub")),
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("toolsList len = %d, want 2", len(list))
	}
	if list[1].Name() != "retriever_stub" {
		t.Fatalf("tool[1].Name = %q", list[1].Name())
	}
}

func TestBuildAgentConfig_validateToolNames_RetrieverConflict(t *testing.T) {
	c := &agentConfig{
		tools:         []interfaces.Tool{testTool(t, "retriever_stub")},
		retrievers:    []interfaces.Retriever{testRetriever(t, "stub")},
		retrieverMode: RetrieverModeAgentic,
	}
	if err := c.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	retr, err := c.resolveRetrieverTools()
	if err != nil {
		t.Fatal(err)
	}
	tools := append(c.toolRegistry.List(), retr...)
	err = validateToolNames(tools)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildAgentConfig_validateToolNames_nilRetrieverTool(t *testing.T) {
	err := validateToolNames([]interfaces.Tool{nil})
	if err == nil || !strings.Contains(err.Error(), "tool must not be nil") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildAgentConfig_WithRetrievers_nilEntry(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrievers(testRetriever(t, "stub"), nil),
	})
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildAgentConfig_WithRetrievers_emptyClears(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrievers(testRetriever(t, "stub")),
		WithRetrievers(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.retrievers != nil {
		t.Fatalf("retrievers = %v, want nil", cfg.retrievers)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("resolved tools = %v, want none", toolNames(tools))
	}
}

func TestBuildAgentConfig_RetrieverMode_default(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.retrieverMode != RetrieverModeAgentic {
		t.Fatalf("retrieverMode = %q, want %q", cfg.retrieverMode, RetrieverModeAgentic)
	}
}

func TestBuildAgentConfig_RetrieverMode_explicit(t *testing.T) {
	for _, mode := range []RetrieverMode{
		RetrieverModeAgentic,
		RetrieverModePrefetch,
		RetrieverModeHybrid,
	} {
		t.Run(string(mode), func(t *testing.T) {
			cfg, err := buildAgentConfig([]Option{
				WithName("test"),
				withTestTemporal("q"),
				WithLLMClient(testLLM(t)),
				WithRetrieverMode(mode),
			})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.retrieverMode != mode {
				t.Fatalf("retrieverMode = %q, want %q", cfg.retrieverMode, mode)
			}
		})
	}
}

func TestAgentConfigFingerprint_RetrieverModeChangesDigest(t *testing.T) {
	baseOpts := []Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
	}
	build := func(mode RetrieverMode) string {
		t.Helper()
		opts := append(append([]Option(nil), baseOpts...), WithRetrieverMode(mode))
		cfg, err := buildAgentConfig(opts)
		if err != nil {
			t.Fatal(err)
		}
		return agentConfigFingerprint(cfg)
	}
	fpAgentic := build(RetrieverModeAgentic)
	fpPrefetch := build(RetrieverModePrefetch)
	fpHybrid := build(RetrieverModeHybrid)
	if fpAgentic == fpPrefetch {
		t.Fatal("expected different fingerprints for agentic vs prefetch retriever mode")
	}
	if fpAgentic == fpHybrid {
		t.Fatal("expected different fingerprints for agentic vs hybrid retriever mode")
	}
	if fpPrefetch == fpHybrid {
		t.Fatal("expected different fingerprints for prefetch vs hybrid retriever mode")
	}
}

func TestBuildAgentConfig_toolsList_includesRetrieverTools_hybrid(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTools(testTool(t, "echo")),
		WithRetrievers(testRetriever(t, "stub")),
		WithRetrieverMode(RetrieverModeHybrid),
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("toolsList len = %d, want 2 (base tool + retriever tool)", len(list))
	}
	if list[1].Name() != "retriever_stub" {
		t.Fatalf("tool[1].Name = %q, want retriever_stub", list[1].Name())
	}
}

func TestResolveTools_order_nativeMemoryThenRAG(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTools(testTool(t, "echo")),
		WithMemory(memory.DefaultConfig(testMemory(t))),
		WithRetrievers(testRetriever(t, "stub")),
		WithRetrieverMode(RetrieverModeAgentic),
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("len=%d want 3", len(list))
	}
	if list[0].Name() != "echo" {
		t.Fatalf("tool[0]=%q want echo", list[0].Name())
	}
	if list[1].Name() != types.SaveMemoryToolName {
		t.Fatalf("tool[1]=%q want %s", list[1].Name(), types.SaveMemoryToolName)
	}
	if list[2].Name() != "retriever_stub" {
		t.Fatalf("tool[2]=%q want retriever_stub (RAG last)", list[2].Name())
	}
}

func TestAgentConfigFingerprint_AgenticRetrieverNamesChangesDigest(t *testing.T) {
	baseOpts := []Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrieverMode(RetrieverModeAgentic),
	}
	cfgNoR, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfgWithR, err := buildAgentConfig(append(baseOpts, WithRetrievers(testRetriever(t, "wiki"))))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNoR) == agentConfigFingerprint(cfgWithR) {
		t.Fatal("expected different fingerprints for agentic mode with vs without retriever names")
	}
}

func TestBuildAgentConfig_RetrieverMode_invalid(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrieverMode(RetrieverMode("bogus")),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid retriever mode") {
		t.Fatalf("got %v", err)
	}
}

// ---------------------------------------------------------------------------
// A2A config tests
// ---------------------------------------------------------------------------

func TestValidateA2AClients(t *testing.T) {
	t.Run("nil_client", func(t *testing.T) {
		err := validateA2AClients([]interfaces.A2AClient{nil})
		if err == nil || !strings.Contains(err.Error(), "nil") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty_name", func(t *testing.T) {
		err := validateA2AClients([]interfaces.A2AClient{testA2AClient(t, "  ", nil)})
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("duplicate_name", func(t *testing.T) {
		c1 := testA2AClient(t, "agent", nil)
		c2 := testA2AClient(t, "agent", nil)
		err := validateA2AClients([]interfaces.A2AClient{c1, c2})
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestBuildAgentConfig_WithA2AConfig(t *testing.T) {
	url := newTestA2ACardServer(t, []a2a.AgentSkill{
		{ID: "search", Name: "Search", Description: "search tool"},
		{ID: "summarize", Name: "Summarize", Description: "summarize tool"},
	})
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithA2AConfig(A2AServers{"agent": A2AConfig{URL: url}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools len = %d, want 2", len(tools))
	}
	if tools[0].Name() != "a2a_agent_search" {
		t.Errorf("tool[0].Name = %q, want a2a_agent_search", tools[0].Name())
	}
	if tools[1].Name() != "a2a_agent_summarize" {
		t.Errorf("tool[1].Name = %q, want a2a_agent_summarize", tools[1].Name())
	}
}

func TestBuildAgentConfig_WithA2AConfig_SkillFilter(t *testing.T) {
	url := newTestA2ACardServer(t, []a2a.AgentSkill{
		{ID: "keep", Name: "Keep", Description: "keep"},
		{ID: "drop", Name: "Drop", Description: "drop"},
	})
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithA2AConfig(A2AServers{"agent": A2AConfig{
			URL:         url,
			SkillFilter: types.A2ASkillFilter{AllowSkills: []string{"keep"}},
		}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "a2a_agent_keep" {
		t.Fatalf("tools = %v, want [a2a_agent_keep]", tools)
	}
}

func TestBuildAgentConfig_WithA2AClients(t *testing.T) {
	cl := testA2AClient(t, "agent1", []interfaces.A2ASkillSpec{{ID: "echo", Description: "echo back"}})
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithA2AClients(cl),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "a2a_agent1_echo" {
		t.Fatalf("tools = %v, want [a2a_agent1_echo]", tools)
	}
}

func TestBuildAgentConfig_WithA2ADefaultServer(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithA2ADefaultServer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.a2aServerConfig == nil {
		t.Fatal("expected a2aServerConfig")
	}
	if cfg.a2aServerConfig.Hostname != defaultA2AHostname || cfg.a2aServerConfig.Port != defaultA2APort {
		t.Fatalf("defaults: got %+v, want hostname=%q port=%d", cfg.a2aServerConfig, defaultA2AHostname, defaultA2APort)
	}
	if len(cfg.a2aServerConfig.BearerTokens) != 0 {
		t.Fatalf("BearerTokens should be empty by default, got %v", cfg.a2aServerConfig.BearerTokens)
	}
}

func TestBuildAgentConfig_WithA2AServer(t *testing.T) {
	t.Run("custom_host_port_and_bearer_tokens", func(t *testing.T) {
		cfg, err := buildAgentConfig([]Option{
			WithName("test"),
			withTestTemporal("q"),
			WithLLMClient(testLLM(t)),
			WithA2AServer(&A2AServerConfig{
				Hostname:     "0.0.0.0",
				Port:         8080,
				BearerTokens: []string{"alpha", "beta"},
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		s := cfg.a2aServerConfig
		if s == nil || s.Hostname != "0.0.0.0" || s.Port != 8080 || len(s.BearerTokens) != 2 {
			t.Fatalf("got %+v", s)
		}
		if s.BearerTokens[0] != "alpha" || s.BearerTokens[1] != "beta" {
			t.Fatalf("BearerTokens = %v", s.BearerTokens)
		}
	})

	t.Run("nil_config_same_as_defaults", func(t *testing.T) {
		cfg, err := buildAgentConfig([]Option{
			WithName("test"),
			withTestTemporal("q"),
			WithLLMClient(testLLM(t)),
			WithA2AServer(nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.a2aServerConfig == nil {
			t.Fatal("expected a2aServerConfig")
		}
		if cfg.a2aServerConfig.Hostname != defaultA2AHostname || cfg.a2aServerConfig.Port != defaultA2APort {
			t.Fatalf("nil config should default hostname/port: got %+v", cfg.a2aServerConfig)
		}
	})

	t.Run("empty_hostname_gets_default", func(t *testing.T) {
		cfg, err := buildAgentConfig([]Option{
			WithName("test"),
			withTestTemporal("q"),
			WithLLMClient(testLLM(t)),
			WithA2AServer(&A2AServerConfig{Hostname: "", Port: 4000}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.a2aServerConfig.Hostname != defaultA2AHostname || cfg.a2aServerConfig.Port != 4000 {
			t.Fatalf("got %+v", cfg.a2aServerConfig)
		}
	})

	t.Run("zero_port_gets_default", func(t *testing.T) {
		cfg, err := buildAgentConfig([]Option{
			WithName("test"),
			withTestTemporal("q"),
			WithLLMClient(testLLM(t)),
			WithA2AServer(&A2AServerConfig{Hostname: "127.0.0.1", Port: 0}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.a2aServerConfig.Hostname != "127.0.0.1" || cfg.a2aServerConfig.Port != defaultA2APort {
			t.Fatalf("got %+v", cfg.a2aServerConfig)
		}
	})

	t.Run("later_WithA2AServer_overrides_WithA2ADefaultServer", func(t *testing.T) {
		cfg, err := buildAgentConfig([]Option{
			WithName("test"),
			withTestTemporal("q"),
			WithLLMClient(testLLM(t)),
			WithA2ADefaultServer(),
			WithA2AServer(&A2AServerConfig{Hostname: "custom.example", Port: 1111}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.a2aServerConfig.Hostname != "custom.example" || cfg.a2aServerConfig.Port != 1111 {
			t.Fatalf("got %+v", cfg.a2aServerConfig)
		}
	})
}

// TestAgentConfigFingerprint_InboundA2AServerIgnored documents that Temporal agent fingerprint
// hashes outbound A2A client wiring only; inbound RunA2A listen config (including BearerTokens)
// must not affect caller/worker digest comparison.
func TestAgentConfigFingerprint_InboundA2AServerIgnored(t *testing.T) {
	base := []Option{
		WithName("fp-test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
	}
	cfgNoInbound, err := buildAgentConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	cfgWithInbound, err := buildAgentConfig(append(base,
		WithA2AServer(&A2AServerConfig{
			Hostname:     "0.0.0.0",
			Port:         7777,
			BearerTokens: []string{"secret-one", "secret-two"},
		}),
	))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNoInbound) != agentConfigFingerprint(cfgWithInbound) {
		t.Fatalf("inbound A2AServerConfig should not change agent fingerprint: %q vs %q",
			agentConfigFingerprint(cfgNoInbound), agentConfigFingerprint(cfgWithInbound))
	}
}

func TestBuildAgentConfig_WithA2AConfig_URLRequired(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithA2AConfig(A2AServers{"agent": A2AConfig{URL: ""}}),
	})
	if err == nil || !strings.Contains(err.Error(), "URL is required") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildAgentConfig_A2A_duplicateClientName(t *testing.T) {
	// Config creates a client named "dup"; explicit client also named "dup" → duplicate.
	// "http://127.0.0.1:1" is a non-routable address; NewClient is lazy so no network call is made.
	cl := testA2AClient(t, "dup", []interfaces.A2ASkillSpec{{ID: "s", Description: "s"}})
	_, err := buildAgentConfig([]Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithA2AConfig(A2AServers{"dup": A2AConfig{URL: "http://127.0.0.1:1"}}),
		WithA2AClients(cl),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate a2a client name") && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
}

func TestAgentConfig_toolsList_includesA2ATools(t *testing.T) {
	echo := testTool(t, "echo")
	a2aTool := NewA2ATool("agent1", interfaces.ToolSpec{Name: "search", Description: "d"}, interfaces.A2ASkillSpec{}, nil)
	c := &agentConfig{
		tools: []interfaces.Tool{echo},
	}
	if err := c.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	list := append(c.toolRegistry.List(), a2aTool)
	if len(list) != 2 {
		t.Fatalf("toolsList len = %d, want 2", len(list))
	}
	if list[0].Name() != "echo" {
		t.Errorf("list[0].Name = %q, want echo", list[0].Name())
	}
	if list[1].Name() != "a2a_agent1_search" {
		t.Errorf("list[1].Name = %q, want a2a_agent1_search", list[1].Name())
	}
}

func TestAgentConfig_validateToolNames_A2AConflict(t *testing.T) {
	a2aTool := NewA2ATool("srv", interfaces.ToolSpec{Name: "s", Description: "d"}, interfaces.A2ASkillSpec{}, nil)
	c := &agentConfig{
		tools: []interfaces.Tool{testTool(t, a2aTool.Name())},
	}
	if err := c.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	tools := append(c.toolRegistry.List(), a2aTool)
	err := validateToolNames(tools)
	if err == nil || (!strings.Contains(err.Error(), "duplicate tool name") && !strings.Contains(err.Error(), "conflicts")) {
		t.Fatalf("want duplicate/conflict error, got %v", err)
	}
}

func TestObservabilityConfigFingerprint_defaultProtocolMatchesExplicitGRPC(t *testing.T) {
	implicit := observabilityConfigFingerprint(&ObservabilityConfig{Endpoint: "localhost:4317"})
	explicit := observabilityConfigFingerprint(&ObservabilityConfig{
		Endpoint: "localhost:4317",
		Protocol: OTLPProtocolGRPC,
	})
	if implicit != explicit {
		t.Fatalf("empty Protocol should fingerprint same as grpc: %q vs %q", implicit, explicit)
	}
}

func TestObservabilityConfigFingerprint_protocolAndInsecureChangeDigest(t *testing.T) {
	base := observabilityConfigFingerprint(&ObservabilityConfig{Endpoint: "localhost:4317"})
	withHTTP := observabilityConfigFingerprint(&ObservabilityConfig{
		Endpoint: "localhost:4317",
		Protocol: OTLPProtocolHTTP,
	})
	if base == withHTTP {
		t.Fatal("expected different digest when Protocol changes")
	}
	insecure := observabilityConfigFingerprint(&ObservabilityConfig{
		Endpoint: "localhost:4317",
		Insecure: true,
	})
	if base == insecure {
		t.Fatal("expected different digest when Insecure changes")
	}
}

func TestObservabilityOptions_appliesTypesDefaults(t *testing.T) {
	oc := &ObservabilityConfig{
		Endpoint: "collector.example:4317",
		Protocol: OTLPProtocolHTTP,
		Insecure: true,
	}
	ac := &agentConfig{
		Name:                "my-agent",
		logger:              logger.DefaultLogger("error"),
		observabilityConfig: oc,
	}
	cfg, err := observability.BuildConfig(observabilityOptions(ac)...)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "my-agent" || cfg.Endpoint != oc.Endpoint {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Protocol != observability.ProtocolHTTP {
		t.Fatalf("Protocol = %q", cfg.Protocol)
	}
	if !cfg.Insecure {
		t.Fatal("want Insecure true")
	}
	if cfg.ExportTimeout != types.DefaultOTLPExportTimeout {
		t.Fatalf("ExportTimeout = %v", cfg.ExportTimeout)
	}
	if cfg.BatchTimeout != types.DefaultOTLPBatchTimeout {
		t.Fatalf("BatchTimeout = %v", cfg.BatchTimeout)
	}
	if cfg.MetricsInterval != types.DefaultOTLPMetricsInterval {
		t.Fatalf("MetricsInterval = %v", cfg.MetricsInterval)
	}
}

func TestBuildAgentConfig_WithObservabilityConfig_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	cfg, err := buildAgentConfig([]Option{
		WithName("obs-agent"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint: host,
			Protocol: OTLPProtocolHTTP,
			Insecure: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.tracer == nil || cfg.metrics == nil {
		t.Fatalf("want tracer and metrics from observability config, tracer=%v metrics=%v", cfg.tracer, cfg.metrics)
	}
	if _, ok := cfg.logs.(*observability.Logs); !ok {
		t.Fatalf("want OTLP *observability.Logs from observability config, got %T", cfg.logs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cfg.tracer.Shutdown(ctx); err != nil {
		t.Errorf("tracer Shutdown: %v", err)
	}
	if err := cfg.metrics.Shutdown(ctx); err != nil {
		t.Errorf("metrics Shutdown: %v", err)
	}
	if err := cfg.logs.Shutdown(ctx); err != nil {
		t.Errorf("logs Shutdown: %v", err)
	}
}

func TestBuildAgentConfig_WithObservabilityConfig_DisableTraces_keepsInjectedTracer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	stub := testTracer(t)
	cfg, err := buildAgentConfig([]Option{
		WithName("disable-traces-keep-tracer"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTracer(stub),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint:      host,
			Protocol:      OTLPProtocolHTTP,
			Insecure:      true,
			DisableTraces: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.tracer != stub {
		t.Fatalf("expected WithTracer to remain when DisableTraces=true, got %T", cfg.tracer)
	}
}

func TestBuildAgentConfig_WithObservabilityConfig_DisableMetrics_keepsInjectedMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	stub := testMetrics(t)
	cfg, err := buildAgentConfig([]Option{
		WithName("disable-metrics-keep-metrics"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMetrics(stub),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint:       host,
			Protocol:       OTLPProtocolHTTP,
			Insecure:       true,
			DisableMetrics: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.metrics != stub {
		t.Fatalf("expected WithMetrics to remain when DisableMetrics=true, got %T", cfg.metrics)
	}
}

func TestBuildAgentConfig_WithObservabilityConfig_replacesInjectedTracer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	tr, err := observability.NewTracer(
		observability.WithEndpoint(host),
		observability.WithName("pre-inject-tracer"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tr.Shutdown(ctx)
	}()

	cfg, err := buildAgentConfig([]Option{
		WithName("obs-replace-tracer"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTracer(tr),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint: host,
			Protocol: OTLPProtocolHTTP,
			Insecure: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.tracer == tr {
		t.Fatal("expected observability-built tracer to replace injected WithTracer, same pointer")
	}
	if _, ok := cfg.tracer.(*observability.Tracer); !ok {
		t.Fatalf("want *observability.Tracer from observability config, got %T", cfg.tracer)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cfg.tracer.Shutdown(ctx)
}

func TestBuildAgentConfig_WithObservabilityConfig_replacesInjectedMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	mt, err := observability.NewMetrics(
		observability.WithEndpoint(host),
		observability.WithName("pre-inject-metrics"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
		observability.WithMetricsInterval(40*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = mt.Shutdown(ctx)
	}()

	cfg, err := buildAgentConfig([]Option{
		WithName("obs-replace-metrics"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithMetrics(mt),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint: host,
			Protocol: OTLPProtocolHTTP,
			Insecure: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.metrics == mt {
		t.Fatal("expected observability-built metrics to replace injected WithMetrics, same pointer")
	}
	if _, ok := cfg.metrics.(*observability.Metrics); !ok {
		t.Fatalf("want *observability.Metrics from observability config, got %T", cfg.metrics)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cfg.metrics.Shutdown(ctx)
}

func TestBuildAgentConfig_WithObservabilityConfig_replacesInjectedTracerMetricsLogsTogether(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	tr, err := observability.NewTracer(
		observability.WithEndpoint(host),
		observability.WithName("triple-pre-tr"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	mt, err := observability.NewMetrics(
		observability.WithEndpoint(host),
		observability.WithName("triple-pre-mt"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
		observability.WithMetricsInterval(40*time.Millisecond),
	)
	if err != nil {
		_ = tr.Shutdown(context.Background())
		t.Fatal(err)
	}
	lg, err := observability.NewLogs(
		observability.WithEndpoint(host),
		observability.WithName("triple-pre-lg"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
	)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tr.Shutdown(ctx)
		_ = mt.Shutdown(ctx)
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tr.Shutdown(ctx)
		_ = mt.Shutdown(ctx)
		_ = lg.Shutdown(ctx)
	}()

	cfg, err := buildAgentConfig([]Option{
		WithName("triple-replace"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithTracer(tr),
		WithMetrics(mt),
		WithLogs(lg),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint: host,
			Protocol: OTLPProtocolHTTP,
			Insecure: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.tracer == tr || cfg.metrics == mt || cfg.logs == lg {
		t.Fatalf("expected all three signals replaced by observability build: tracerSame=%v metricsSame=%v logsSame=%v",
			cfg.tracer == tr, cfg.metrics == mt, cfg.logs == lg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cfg.tracer.Shutdown(ctx)
	_ = cfg.metrics.Shutdown(ctx)
	_ = cfg.logs.Shutdown(ctx)
}

func TestBuildAgentConfig_injectedStubLogs_withoutObs_doesNotWireOtelLogger(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("stub-logs-no-otel"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogs(testLogs(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if otlpLogsClientConfigured(cfg.logs) {
		t.Fatal("stub WithLogs must not count as OTLP *observability.Logs for logger wiring")
	}
}

func TestBuildAgentConfig_WithLogs_withoutObservability(t *testing.T) {
	stub := testLogs(t)
	cfg, err := buildAgentConfig([]Option{
		WithName("logs-inject"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogs(stub),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logs != stub {
		t.Fatalf("expected injected WithLogs to be kept without WithObservabilityConfig, got %T", cfg.logs)
	}
}

func TestBuildAgentConfig_WithObservabilityConfig_overwritesWithLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	stub := testLogs(t)
	cfg, err := buildAgentConfig([]Option{
		WithName("obs-overwrites-logs"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogs(stub),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint: host,
			Protocol: OTLPProtocolHTTP,
			Insecure: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.logs.(*observability.Logs); !ok {
		t.Fatalf("expected built OTLP *observability.Logs to replace WithLogs, got %T", cfg.logs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cfg.logs.Shutdown(ctx)
}

func TestBuildAgentConfig_NewLogs_injected_alone_autoWiresDefaultLogger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	lg, err := observability.NewLogs(
		observability.WithEndpoint(host),
		observability.WithName("inject-only-logs"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Shutdown(ctx)
	}()

	cfg, err := buildAgentConfig([]Option{
		WithName("inject-logs-wire"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogs(lg),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logs != lg {
		t.Fatalf("expected WithLogs instance to be kept without observability config, got %T", cfg.logs)
	}
	ctx := context.Background()
	cfg.logger.Info(ctx, "smoke after auto-wire")
}

func TestBuildAgentConfig_WithObservabilityConfig_replacesInjectedOTLPLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	lg, err := observability.NewLogs(
		observability.WithEndpoint(host),
		observability.WithName("pre-inject"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Shutdown(ctx)
	}()

	cfg, err := buildAgentConfig([]Option{
		WithName("obs-replace-inject"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogs(lg),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint: host,
			Protocol: OTLPProtocolHTTP,
			Insecure: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logs == lg {
		t.Fatal("expected observability-built Logs to replace injected WithLogs, not the same pointer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cfg.logs.Shutdown(ctx)
}

func TestBuildAgentConfig_WithObservabilityConfig_DisableLogs_keepsWithLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	stub := testLogs(t)
	cfg, err := buildAgentConfig([]Option{
		WithName("disable-logs-keep-inject"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogs(stub),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint:    host,
			Protocol:    OTLPProtocolHTTP,
			Insecure:    true,
			DisableLogs: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logs != stub {
		t.Fatalf("expected WithLogs to remain when DisableLogs=true, got %T", cfg.logs)
	}
}

func TestBuildAgentConfig_WithObservabilityConfig_DisableLogs_injectedOTLPLogs_wiresDefaultLogger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	lg, err := observability.NewLogs(
		observability.WithEndpoint(host),
		observability.WithName("disable-logs-otlp-inject"),
		observability.WithProtocol(observability.ProtocolHTTP),
		observability.WithInsecure(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Shutdown(ctx)
	}()

	cfg, err := buildAgentConfig([]Option{
		WithName("disable-logs-wire-otel"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogs(lg),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint:    host,
			Protocol:    OTLPProtocolHTTP,
			Insecure:    true,
			DisableLogs: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logs != lg {
		t.Fatalf("expected injected OTLP Logs to remain when DisableLogs=true, got %T", cfg.logs)
	}
	if !otlpLogsClientConfigured(cfg.logs) {
		t.Fatal("expected *observability.Logs so default logger can bridge to OTLP")
	}
	ctx := context.Background()
	cfg.logger.Info(ctx, "smoke with DisableLogs and injected OTLP Logs")
}

func TestBuildAgentConfig_WithObservabilityConfig_customLogger_warnsAboutLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")

	var buf bytes.Buffer
	custom := logger.NewWriterLogger(&buf, "warn", "text", false)

	_, err := buildAgentConfig([]Option{
		WithName("custom-log-warn"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithLogger(custom),
		WithObservabilityConfig(&ObservabilityConfig{
			Endpoint: host,
			Protocol: OTLPProtocolHTTP,
			Insecure: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "custom WithLogger") {
		t.Fatalf("expected warning about custom WithLogger and OTLP logs; buf=%q", out)
	}
}

func TestBuildAgentConfig_WithExplicitRegistryOptions(t *testing.T) {
	toolReg := NewToolRegistry()
	if err := toolReg.Register(testTool(t, "native")); err != nil {
		t.Fatal(err)
	}
	mcpReg := NewMCPRegistry(nil)
	if err := mcpReg.RegisterClient(testMCPClient(t, "mcp-srv")); err != nil {
		t.Fatal(err)
	}
	a2aReg := NewA2ARegistry(nil)
	if err := a2aReg.RegisterClient(testA2AClient(t, "a2a-srv", nil)); err != nil {
		t.Fatal(err)
	}
	subReg := NewSubAgentRegistry()
	child := &Agent{agentConfig: agentConfig{Name: "Child"}}
	if err := child.buildRegistries(); err != nil {
		t.Fatal(err)
	}
	if err := subReg.Register(child); err != nil {
		t.Fatal(err)
	}

	cfg, err := buildAgentConfig([]Option{
		WithName("parent"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithToolRegistry(toolReg),
		WithMCPRegistry(mcpReg),
		WithA2ARegistry(a2aReg),
		WithSubAgentRegistry(subReg),
		WithToolApprovalPolicy(AutoToolApprovalPolicy()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.toolRegistry != toolReg {
		t.Fatal("WithToolRegistry should preserve user registry")
	}
	if cfg.mcpRegistry != mcpReg {
		t.Fatal("WithMCPRegistry should preserve user registry")
	}
	if cfg.a2aRegistry != a2aReg {
		t.Fatal("WithA2ARegistry should preserve user registry")
	}
	if cfg.subAgentRegistry != subReg {
		t.Fatal("WithSubAgentRegistry should preserve user registry")
	}

	a := &Agent{agentConfig: *cfg}
	if a.ToolRegistry() != toolReg || a.MCPRegistry() != mcpReg || a.A2ARegistry() != a2aReg || a.SubAgentRegistry() != subReg {
		t.Fatal("registry accessors should return configured registries")
	}
	tools, err := cfg.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < 2 {
		t.Fatalf("resolveTools len = %d, want at least native + sub-agent tools", len(tools))
	}
}

func TestWithBudget_Validation(t *testing.T) {
	approvalHandler := func(context.Context, *types.ApprovalRequest) {}
	cases := []struct {
		name    string
		opts    []Option
		wantErr string
	}{
		{
			name: "valid MaxTokens only",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithBudget(BudgetConfig{MaxTokens: 500, OnExceeded: BudgetStopRun}),
			},
		},
		{
			name: "valid MaxCostUSD with rates",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithBudget(BudgetConfig{
					MaxCostUSD:         1.0,
					PromptUSDPer1M:     3.0,
					CompletionUSDPer1M: 15.0,
					OnExceeded:         BudgetStopRun,
				}),
			},
		},
		{
			name: "valid WaitForApproval with handler",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithApprovalHandler(approvalHandler),
				WithBudget(BudgetConfig{MaxTokens: 500, OnExceeded: BudgetWaitForApproval}),
			},
		},
		{
			name: "no limits set",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithBudget(BudgetConfig{OnExceeded: BudgetStopRun}),
			},
			wantErr: "at least one of MaxTokens or MaxCostUSD",
		},
		{
			name: "MaxCostUSD without rates",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithBudget(BudgetConfig{MaxCostUSD: 1.0}),
			},
			wantErr: "PromptUSDPer1M and CompletionUSDPer1M",
		},
		{
			// ApprovalHandler is no longer required at NewAgent for BudgetWaitForApproval;
			// it is validated at Run() call time so stream-only callers need no handler.
			name: "WaitForApproval without handler is valid at NewAgent",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithBudget(BudgetConfig{MaxTokens: 500, OnExceeded: BudgetWaitForApproval}),
			},
			wantErr: "", // no error expected — handler check deferred to Run()
		},
		{
			name: "unknown action",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithBudget(BudgetConfig{MaxTokens: 500, OnExceeded: BudgetExceededAction("explode")}),
			},
			wantErr: "unknown OnExceeded action",
		},
		{
			name: "negative ApprovalExtraTokens",
			opts: []Option{
				WithName("budget-agent"), WithLLMClient(testLLM(t)),
				WithBudget(BudgetConfig{MaxTokens: 500, ApprovalExtraTokens: -1}),
			},
			wantErr: "ApprovalExtraTokens must be >= 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAgentConfig(tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error %q to contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

func TestWithBudget_DefaultsApprovalExtraFromMax(t *testing.T) {
	approvalHandler := func(context.Context, *types.ApprovalRequest) {}
	cfg, err := buildAgentConfig([]Option{
		WithName("budget-agent"),
		WithLLMClient(testLLM(t)),
		WithApprovalHandler(approvalHandler),
		WithBudget(BudgetConfig{
			MaxTokens:          500,
			MaxCostUSD:         1.0,
			PromptUSDPer1M:     3.0,
			CompletionUSDPer1M: 15.0,
			OnExceeded:         BudgetWaitForApproval,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.budgetConfig.ApprovalExtraTokens != 500 {
		t.Fatalf("ApprovalExtraTokens: got %d want 500", cfg.budgetConfig.ApprovalExtraTokens)
	}
	if cfg.budgetConfig.ApprovalExtraCostUSD != 1.0 {
		t.Fatalf("ApprovalExtraCostUSD: got %v want 1.0", cfg.budgetConfig.ApprovalExtraCostUSD)
	}
}

func TestWithBudget_PreservesExplicitApprovalExtra(t *testing.T) {
	approvalHandler := func(context.Context, *types.ApprovalRequest) {}
	cfg, err := buildAgentConfig([]Option{
		WithName("budget-agent"),
		WithLLMClient(testLLM(t)),
		WithApprovalHandler(approvalHandler),
		WithBudget(BudgetConfig{
			MaxTokens:           500,
			ApprovalExtraTokens: 100,
			OnExceeded:          BudgetWaitForApproval,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.budgetConfig.ApprovalExtraTokens != 100 {
		t.Fatalf("ApprovalExtraTokens: got %d want 100", cfg.budgetConfig.ApprovalExtraTokens)
	}
}

func TestWithBudget_StopRunRejectsApprovalExtra(t *testing.T) {
	// Setting ApprovalExtraTokens with BudgetStopRun is now an explicit validation error
	// rather than silent clearing, so callers are not surprised.
	_, err := buildAgentConfig([]Option{
		WithName("budget-agent"),
		WithLLMClient(testLLM(t)),
		WithBudget(BudgetConfig{
			MaxTokens:           500,
			ApprovalExtraTokens: 100,
			OnExceeded:          BudgetStopRun,
		}),
	})
	if err == nil {
		t.Fatal("expected validation error for ApprovalExtraTokens with BudgetStopRun, got nil")
	}
	if !strings.Contains(err.Error(), "BudgetWaitForApproval") {
		t.Fatalf("expected error to mention BudgetWaitForApproval, got: %v", err)
	}
}

func TestWithBudget_FingerprintIncludesBudget(t *testing.T) {
	baseOpts := []Option{
		WithName("fp-agent"), WithLLMClient(testLLM(t)),
	}
	cfg0, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	fp0 := agentConfigFingerprint(cfg0)

	cfg1, err := buildAgentConfig(append(baseOpts, WithBudget(BudgetConfig{
		MaxTokens:  1000,
		OnExceeded: BudgetStopRun,
	})))
	if err != nil {
		t.Fatal(err)
	}
	fp1 := agentConfigFingerprint(cfg1)

	if fp0 == fp1 {
		t.Fatal("fingerprint should differ when budget is added")
	}
}

func TestWithBudget_DefaultsOnExceededToStopRun(t *testing.T) {
	cfg, err := buildAgentConfig([]Option{
		WithName("budget-agent"),
		WithLLMClient(testLLM(t)),
		WithBudget(BudgetConfig{MaxTokens: 500}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.budgetConfig.OnExceeded != BudgetStopRun {
		t.Fatalf("expected OnExceeded to default to BudgetStopRun, got %q", cfg.budgetConfig.OnExceeded)
	}
}

func TestParseBudgetApproval_PublicAPI(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameBudget,
		Value: map[string]any{
			"agentName":     "pub",
			"totalTokens":   float64(99),
			"approvalToken": "tok",
		},
	}
	got, err := ParseBudgetApproval(req)
	if err != nil {
		t.Fatalf("ParseBudgetApproval: %v", err)
	}
	if got.AgentName != "pub" || got.TotalTokens != 99 {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestParseCustomEventBudget_PublicAPI(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(AgentCustomEventNameBudget), map[string]any{
		"agentName":     "pub",
		"totalTokens":   float64(42),
		"costUsd":       0.002,
		"approvalToken": "tok-ce",
	})
	got, err := ParseCustomEventBudget(ev)
	if err != nil {
		t.Fatalf("ParseCustomEventBudget: %v", err)
	}
	if got.AgentName != "pub" || got.TotalTokens != 42 || got.ApprovalToken != "tok-ce" {
		t.Fatalf("unexpected: %#v", got)
	}
}
