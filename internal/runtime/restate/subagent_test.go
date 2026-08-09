package restate

import (
	"reflect"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/x/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func testRestateRuntime(name string) *RestateRuntime {
	return &RestateRuntime{
		Runtime: base.Runtime{
			AgentSpec: sdkruntime.AgentSpec{Name: name, SystemPrompt: "p"},
			AgentConfig: sdkruntime.AgentConfig{
				Limits: sdkruntime.AgentLimits{MaxIterations: 3},
			},
			ToolExecutionMode: types.AgentToolExecutionModeParallel,
			Tracer:            observability.DefaultNoopTracer,
			Metrics:           observability.DefaultNoopMetrics,
		},
		logger:               logger.NoopLogger(),
		agentLoopServiceName: serviceName(agentLoopServiceName, name),
		eventLogServiceName:  serviceName(agentEventLogServiceName, name),
		config: &RestateConfig{Ingress: IngressConfig{
			URL:             "http://localhost:8080",
			HTTPTimeout:     2 * time.Second,
			HTTPMaxAttempts: 2,
		}},
	}
}

func TestBuildSubAgentRoutes_setsNameToolAndService(t *testing.T) {
	subRT := testRestateRuntime("Sub")
	routes := buildSubAgentRoutes([]*sdkruntime.SubAgentSpec{{
		Name:     "Sub",
		ToolName: "subagent_Sub",
		Runtime:  subRT,
	}})
	route, ok := routes["subagent_Sub"]
	if !ok {
		t.Fatal("missing route")
	}
	if route.Name != "Sub" {
		t.Fatalf("name: got %q want Sub", route.Name)
	}
	if route.ToolName != "subagent_Sub" {
		t.Fatalf("tool name: got %q", route.ToolName)
	}
	if route.ServiceName != "AgentLoop_Sub" {
		t.Fatalf("service name: got %q want AgentLoop_Sub", route.ServiceName)
	}
}

func TestBuildSubAgentRoutes_nestedChildRoutes(t *testing.T) {
	childRT := testRestateRuntime("Child")
	parentRT := testRestateRuntime("Parent")
	routes := buildSubAgentRoutes([]*sdkruntime.SubAgentSpec{{
		Name:     "Parent",
		ToolName: "subagent_Parent",
		Runtime:  parentRT,
		Children: []*sdkruntime.SubAgentSpec{{
			Name:     "Child",
			ToolName: "subagent_Child",
			Runtime:  childRT,
		}},
	}})

	parentRoute := routes["subagent_Parent"]
	childRoute, ok := parentRoute.ChildRoutes["subagent_Child"]
	if !ok {
		t.Fatal("missing nested child route")
	}
	if childRoute.Name != "Child" {
		t.Fatalf("child name: got %q", childRoute.Name)
	}
	if childRoute.ServiceName != "AgentLoop_Child" {
		t.Fatalf("child service: got %q", childRoute.ServiceName)
	}
}

func TestBuildSubAgentRoutes_nonRestateSkipped(t *testing.T) {
	routes := buildSubAgentRoutes([]*sdkruntime.SubAgentSpec{{
		Name:     "Local",
		ToolName: "subagent_Local",
		Runtime:  nil,
	}})
	if len(routes) != 0 {
		t.Fatalf("non-restate runtime should be skipped, got %#v", routes)
	}
}

func TestValidateAgentName(t *testing.T) {
	rt := testRestateRuntime("Root")
	if err := rt.validateAgentName(""); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if err := rt.validateAgentName("Root"); err != nil {
		t.Fatalf("self: %v", err)
	}
	if err := rt.validateAgentName("Other"); err == nil {
		t.Fatal("expected error for mismatched name")
	}
}

func TestDelegateToSubAgent_MaxDepth(t *testing.T) {
	rt := testRestateRuntime("root")
	ctx := mocks.NewMockContext(t)
	content, err := rt.delegateToSubAgent(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{SubAgentDepth: 2, MaxSubAgentDepth: 2, RunID: "r"},
	}, base.ToolCallRequest{ToolName: "child", ToolDisplayName: "Child"},
		SubAgentRoute{Name: "Child", ServiceName: "AgentLoop_Child"}, func(events.AgentEvent) {})
	require.NoError(t, err)
	require.Contains(t, content, "maximum nesting depth")
}

func TestDelegateToSubAgent_EmptyServiceName(t *testing.T) {
	rt := testRestateRuntime("root")
	ctx := mocks.NewMockContext(t)
	content, err := rt.delegateToSubAgent(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{MaxSubAgentDepth: 2, RunID: "r"},
	}, base.ToolCallRequest{ToolName: "child", ToolDisplayName: "Child"},
		SubAgentRoute{Name: "Child"}, func(events.AgentEvent) {})
	require.NoError(t, err)
	require.Contains(t, content, "AgentLoop service is not configured")
}

func TestInvokeSubAgentHandler_NoTimeout(t *testing.T) {
	rt := testRestateRuntime("root")
	ctx := mocks.NewMockContext(t)
	client := mocks.NewMockClient(t)
	ctx.EXPECT().Service("AgentLoop_Child", agentLoopRunHandler).Return(client)
	want := &AgentLoopResponse{Result: &types.AgentRunResult{Content: "child-out"}}
	client.On("Request", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		reflect.ValueOf(args.Get(1)).Elem().Set(reflect.ValueOf(want))
	}).Return(nil)

	got, err := rt.invokeSubAgentHandler(restatesdk.WithMockContext(ctx), "AgentLoop_Child", agentLoopRunHandler,
		AgentLoopRequest{agentLoopCore: agentLoopCore{RunID: "child"}}, 0)
	require.NoError(t, err)
	require.Equal(t, "child-out", got.Result.Content)
}
