package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/golang/mock/gomock"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	rtmocks "github.com/agenticenv/agent-sdk-go/internal/runtime/mocks"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	ifacemocks "github.com/agenticenv/agent-sdk-go/pkg/interfaces/mocks"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	temporalmocks "go.temporal.io/sdk/mocks"
)

func testAgentWithRuntime(rt runtime.Runtime) *Agent {
	cfg := agentConfig{
		Name:             "TestAgent",
		logger:           logger.DefaultLogger("error"),
		maxSubAgentDepth: 2,
		tracer:           observability.DefaultNoopTracer,
		metrics:          observability.DefaultNoopMetrics,
	}
	if err := cfg.buildRegistries(); err != nil {
		panic(err)
	}
	return &Agent{
		agentConfig: cfg,
		runtime:     rt,
		runs:        store.NewKV[string, *agentRun](),
		streams:     store.NewKV[string, *agentStream](),
	}
}

func mustTestRegistries(t *testing.T, cfg *agentConfig) {
	t.Helper()
	if err := cfg.buildRegistries(); err != nil {
		t.Fatal(err)
	}
}

func closedDoneChan() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// testLLM returns a MockLLMClient with default expectations for build/config tests.
func testLLM(t *testing.T) *ifacemocks.MockLLMClient {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockLLMClient(ctrl)
	m.EXPECT().GetModel().Return("stub").AnyTimes()
	m.EXPECT().GetProvider().Return(interfaces.LLMProviderOpenAI).AnyTimes()
	m.EXPECT().IsStreamSupported().Return(false).AnyTimes()
	m.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(&interfaces.LLMResponse{}, nil).AnyTimes()
	m.EXPECT().GenerateStream(gomock.Any(), gomock.Any()).Return(nil, errors.New("stub")).AnyTimes()
	return m
}

// testStreamLLM is like [testLLM] but reports IsStreamSupported true (A2A streaming paths).
func testStreamLLM(t *testing.T) *ifacemocks.MockLLMClient {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockLLMClient(ctrl)
	m.EXPECT().GetModel().Return("stream-model").AnyTimes()
	m.EXPECT().GetProvider().Return(interfaces.LLMProviderOpenAI).AnyTimes()
	m.EXPECT().IsStreamSupported().Return(true).AnyTimes()
	m.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(&interfaces.LLMResponse{}, nil).AnyTimes()
	m.EXPECT().GenerateStream(gomock.Any(), gomock.Any()).Return(nil, errors.New("unused")).AnyTimes()
	return m
}

// testTool returns a MockTool with default expectations for config/registry tests.
func testTool(t *testing.T, name string) *ifacemocks.MockTool {
	t.Helper()
	return testToolMeta(t, name, "Mock", "mock", interfaces.JSONSchema{})
}

// testToolMeta is like [testTool] with explicit display name, description, and parameters.
func testToolMeta(t *testing.T, name, display, desc string, params interfaces.JSONSchema) *ifacemocks.MockTool {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockTool(ctrl)
	m.EXPECT().Name().Return(name).AnyTimes()
	m.EXPECT().DisplayName().Return(display).AnyTimes()
	m.EXPECT().Description().Return(desc).AnyTimes()
	m.EXPECT().Parameters().Return(params).AnyTimes()
	m.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	return m
}

// testMCPClient returns a MockMCPClient with default expectations for registry tests.
func testMCPClient(t *testing.T, name string) *ifacemocks.MockMCPClient {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockMCPClient(ctrl)
	m.EXPECT().Name().Return(name).AnyTimes()
	m.EXPECT().Ping(gomock.Any()).Return(nil).AnyTimes()
	m.EXPECT().ListTools(gomock.Any()).Return(nil, nil).AnyTimes()
	m.EXPECT().CallTool(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	m.EXPECT().Close().Return(nil).AnyTimes()
	return m
}

// toolWithApproval combines generated Tool + ToolApproval mocks (mockgen emits them separately).
type toolWithApproval struct {
	*ifacemocks.MockTool
	*ifacemocks.MockToolApproval
}

// testToolWithApproval returns a Tool that also implements ToolApproval.
func testToolWithApproval(t *testing.T, name string, needApproval bool) *toolWithApproval {
	t.Helper()
	ctrl := gomock.NewController(t)
	tool := ifacemocks.NewMockTool(ctrl)
	appr := ifacemocks.NewMockToolApproval(ctrl)
	tool.EXPECT().Name().Return(name).AnyTimes()
	tool.EXPECT().DisplayName().Return("Mock").AnyTimes()
	tool.EXPECT().Description().Return("mock").AnyTimes()
	tool.EXPECT().Parameters().Return(interfaces.JSONSchema{}).AnyTimes()
	tool.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	appr.EXPECT().ApprovalRequired().Return(needApproval).AnyTimes()
	return &toolWithApproval{MockTool: tool, MockToolApproval: appr}
}

// testRetriever returns a MockRetriever with default expectations.
func testRetriever(t *testing.T, name string) *ifacemocks.MockRetriever {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockRetriever(ctrl)
	m.EXPECT().Name().Return(name).AnyTimes()
	m.EXPECT().Search(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	return m
}

// testA2AClient returns a MockA2AClient with default expectations.
func testA2AClient(t *testing.T, name string, skills []interfaces.A2ASkillSpec) *ifacemocks.MockA2AClient {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockA2AClient(ctrl)
	m.EXPECT().Name().Return(name).AnyTimes()
	m.EXPECT().Ping(gomock.Any()).Return(nil).AnyTimes()
	m.EXPECT().ResolveCard(gomock.Any()).Return(interfaces.A2AAgentCard{Name: name}, nil).AnyTimes()
	m.EXPECT().ListSkills(gomock.Any()).Return(skills, nil).AnyTimes()
	m.EXPECT().SendMessage(gomock.Any(), gomock.Any()).Return(interfaces.A2ASendMessageResult{}, nil).AnyTimes()
	m.EXPECT().Close().Return(nil).AnyTimes()
	return m
}

// testMemory returns a MockMemory with default expectations.
func testMemory(t *testing.T) *ifacemocks.MockMemory {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockMemory(ctrl)
	m.EXPECT().Store(gomock.Any(), gomock.Any(), gomock.Any()).Return("", nil).AnyTimes()
	m.EXPECT().Load(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	m.EXPECT().Clear(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	return m
}

// testLogs returns a MockLogs (Shutdown may be called).
func testLogs(t *testing.T) *ifacemocks.MockLogs {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := ifacemocks.NewMockLogs(ctrl)
	m.EXPECT().Shutdown(gomock.Any()).Return(nil).AnyTimes()
	return m
}

// testTracer returns a MockTracer for injection/precedence tests (methods unused).
func testTracer(t *testing.T) *ifacemocks.MockTracer {
	t.Helper()
	return ifacemocks.NewMockTracer(gomock.NewController(t))
}

// testMetrics returns a MockMetrics for injection/precedence tests (methods unused).
func testMetrics(t *testing.T) *ifacemocks.MockMetrics {
	t.Helper()
	return ifacemocks.NewMockMetrics(gomock.NewController(t))
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

func TestAgent_ValidateConversationID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	l := logger.DefaultLogger("error")
	a := &Agent{agentConfig: agentConfig{logger: l}}

	if err := a.validateConversationID(""); err != nil {
		t.Errorf("empty conversationID with no conversation: %v", err)
	}
	if err := a.validateConversationID("conv1"); err == nil {
		t.Error("non-empty conversationID with no conversation should error")
	}

	a.conversationConfig = &conversation.Config{Conversation: ifacemocks.NewMockConversation(ctrl)}
	if err := a.validateConversationID(""); err == nil {
		t.Error("empty conversationID with conversation should error")
	}
	if err := a.validateConversationID("conv1"); err != nil {
		t.Errorf("valid conversationID with conversation: %v", err)
	}
}

func TestBuildSubAgentSpecs_flat(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	childRT := rtmocks.NewMockRuntime(ctrl)
	child := &Agent{agentConfig: agentConfig{Name: "Child"}, runtime: childRT}
	mustTestRegistries(t, &child.agentConfig)
	parentReg := NewSubAgentRegistry()
	_ = parentReg.Register(child)
	parent := &Agent{agentConfig: agentConfig{Name: "Parent", subAgentRegistry: parentReg}, runtime: rtmocks.NewMockRuntime(ctrl)}
	mustTestRegistries(t, &parent.agentConfig)

	got, err := parent.resolveSubAgentSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 spec, got %d", len(got))
	}
	key, err := subAgentToolName(child.Name)
	if err != nil {
		t.Fatal(err)
	}
	spec := got[0]
	if spec.ToolName != key {
		t.Fatalf("ToolName = %q, want %q", spec.ToolName, key)
	}
	if spec.Name != child.Name {
		t.Fatalf("Name = %q, want %q", spec.Name, child.Name)
	}
	if spec.Runtime != childRT {
		t.Fatal("Runtime mismatch")
	}
	if spec.Children != nil {
		t.Fatalf("expected no children, got %v", spec.Children)
	}
}

func TestBuildSubAgentSpecs_nested(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	leafRT := rtmocks.NewMockRuntime(ctrl)
	leaf := &Agent{agentConfig: agentConfig{Name: "Leaf"}, runtime: leafRT}
	mustTestRegistries(t, &leaf.agentConfig)
	midRT := rtmocks.NewMockRuntime(ctrl)
	midReg := NewSubAgentRegistry()
	_ = midReg.Register(leaf)
	mid := &Agent{agentConfig: agentConfig{Name: "Mid", subAgentRegistry: midReg}, runtime: midRT}
	mustTestRegistries(t, &mid.agentConfig)
	rootReg := NewSubAgentRegistry()
	_ = rootReg.Register(mid)
	root := &Agent{agentConfig: agentConfig{Name: "Root", subAgentRegistry: rootReg}, runtime: rtmocks.NewMockRuntime(ctrl)}
	mustTestRegistries(t, &root.agentConfig)

	got, err := root.resolveSubAgentSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 top-level spec, got %d", len(got))
	}
	midSpec := got[0]
	if midSpec.Runtime != midRT {
		t.Fatal("mid Runtime mismatch")
	}
	if len(midSpec.Children) != 1 {
		t.Fatalf("want 1 child spec, got %d", len(midSpec.Children))
	}
	leafSpec := midSpec.Children[0]
	if leafSpec.Runtime != leafRT {
		t.Fatal("leaf Runtime mismatch")
	}
	if len(leafSpec.Children) != 0 {
		t.Fatalf("leaf should have no children, got %d", len(leafSpec.Children))
	}
}

func TestBuildSubAgentSpecs_noRuntimeStillBuilds(t *testing.T) {
	// Sub-agent with no runtime still gets a spec — runtime decides what to do with it.
	sub := &Agent{agentConfig: agentConfig{Name: "X"}}
	mustTestRegistries(t, &sub.agentConfig)
	parentReg := NewSubAgentRegistry()
	_ = parentReg.Register(sub)
	parent := &Agent{agentConfig: agentConfig{subAgentRegistry: parentReg}}
	mustTestRegistries(t, &parent.agentConfig)

	got, err := parent.resolveSubAgentSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 spec, got %v", got)
	}
	if got[0].ToolName != "subagent_X" {
		t.Fatalf("ToolName = %q", got[0].ToolName)
	}
	if got[0].Runtime != nil {
		t.Fatalf("expected nil runtime, got %v", got[0].Runtime)
	}
}

// TestBuildAgent_DisableLocalWorker_Succeeds verifies that DisableLocalWorker builds
// successfully on Temporal. Streaming subscribes to the workflow's WorkflowStream via a
// plain Temporal client (see internal/runtime/temporal Stream), which has no
// dependency on whether the agent process itself embeds a worker.
func TestBuildAgent_DisableLocalWorker_Succeeds(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	a, err := buildAgent([]Option{
		WithName("x"),
		WithTemporalClient(tc, "q"),
		WithLLMClient(testLLM(t)),
		DisableLocalWorker(),
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil agent")
	}
}

func TestAgent_OnApproval(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().Approve(gomock.Any(), "tok", types.ApprovalStatusApproved).Return(nil)

	a := testAgentWithRuntime(mockRT)
	if err := a.OnApproval(context.Background(), "tok", types.ApprovalStatusApproved); err != nil {
		t.Fatal(err)
	}
}

func TestToolsList_picksUpRegistryChange(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	child := &Agent{agentConfig: agentConfig{Name: "Child"}}
	mustTestRegistries(t, &child.agentConfig)
	parentReg := NewSubAgentRegistry()
	parent := &Agent{
		agentConfig: agentConfig{
			Name:             "Parent",
			toolRegistry:     NewToolRegistry(),
			subAgentRegistry: parentReg,
			logger:           NoopLogger(),
		},
		runtime: rtmocks.NewMockRuntime(ctrl),
	}
	if err := parent.buildMCPRegistry(); err != nil {
		t.Fatal(err)
	}
	if err := parent.buildA2ARegistry(); err != nil {
		t.Fatal(err)
	}
	_ = parent.toolRegistry.Register(testTool(t, "echo"))

	tools1, err := parent.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools1) != 1 {
		t.Fatalf("tools1 = %d, want 1", len(tools1))
	}

	_ = parent.subAgentRegistry.Register(child)
	tools2, err := parent.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools2) != 2 {
		t.Fatalf("tools2 = %d, want 2 after sub-agent register", len(tools2))
	}
	subAgents, err := parent.resolveSubAgentSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(subAgents) != 1 {
		t.Fatalf("subAgents = %d, want 1", len(subAgents))
	}
	if agentConfigFingerprintTools(&parent.agentConfig, tools1) == agentConfigFingerprintTools(&parent.agentConfig, tools2) {
		t.Fatal("run fingerprint should change when tools change")
	}
}

func TestToolsList_stableOrder(t *testing.T) {
	c := &agentConfig{
		toolRegistry: NewToolRegistry(),
	}
	_ = c.toolRegistry.Register(testTool(t, "b"))
	_ = c.toolRegistry.Register(testTool(t, "a"))
	tools1, err := c.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fp1 := agentConfigFingerprintTools(c, tools1)
	tools2, err := c.resolveTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fp2 := agentConfigFingerprintTools(c, tools2)
	if fp1 != fp2 {
		t.Fatalf("fingerprints differ: %q vs %q", fp1, fp2)
	}
}

func TestAgent_RegistryAccessors(t *testing.T) {
	if (*Agent)(nil).ToolRegistry() != nil {
		t.Fatal("nil agent ToolRegistry should be nil")
	}
	if (*Agent)(nil).MCPRegistry() != nil {
		t.Fatal("nil agent MCPRegistry should be nil")
	}
	if (*Agent)(nil).A2ARegistry() != nil {
		t.Fatal("nil agent A2ARegistry should be nil")
	}
	if (*Agent)(nil).SubAgentRegistry() != nil {
		t.Fatal("nil agent SubAgentRegistry should be nil")
	}

	toolReg := NewToolRegistry()
	mcpReg := NewMCPRegistry(nil)
	a2aReg := NewA2ARegistry(nil)
	subReg := NewSubAgentRegistry()
	child := &Agent{agentConfig: agentConfig{Name: "Child", taskQueue: "q-child"}}
	mustTestRegistries(t, &child.agentConfig)
	if err := subReg.Register(child); err != nil {
		t.Fatal(err)
	}

	a := &Agent{
		agentConfig: agentConfig{
			Name:             "Parent",
			toolRegistry:     toolReg,
			mcpRegistry:      mcpReg,
			a2aRegistry:      a2aReg,
			subAgentRegistry: subReg,
		},
	}
	if a.ToolRegistry() != toolReg {
		t.Fatal("ToolRegistry accessor should return configured registry")
	}
	if a.MCPRegistry() != mcpReg {
		t.Fatal("MCPRegistry accessor should return configured registry")
	}
	if a.A2ARegistry() != a2aReg {
		t.Fatal("A2ARegistry accessor should return configured registry")
	}
	if a.SubAgentRegistry() != subReg {
		t.Fatal("SubAgentRegistry accessor should return configured registry")
	}
}
