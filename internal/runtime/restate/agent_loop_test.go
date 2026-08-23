package restate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/x/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestConversationMemoryEnabled(t *testing.T) {
	rt := testRestateRuntime("a")
	if rt.conversationMemoryEnabled(AgentLoopInput{}) {
		t.Fatal("empty conversation should be disabled")
	}
	if rt.conversationMemoryEnabled(AgentLoopInput{agentLoopCore: agentLoopCore{ConversationID: "c1"}}) {
		t.Fatal("no conversation store → disabled")
	}
}

func TestToolMessageConstants(t *testing.T) {
	for _, s := range []string{
		msgToolRejected,
		msgToolApprovalUnavailable,
		msgToolUnauthorized,
		msgToolApprovalTimedOut,
	} {
		if s == "" {
			t.Fatal("expected non-empty tool message constants")
		}
	}
	_ = types.ApprovalStatusApproved
}

func TestEventAllowed(t *testing.T) {
	ev := events.NewAgentTextMessageContentEvent("mid", "hello")
	require.False(t, eventAllowed(nil, ev))
	require.False(t, eventAllowed([]events.AgentEventType{events.AgentEventTypeCustom}, nil))
	require.True(t, eventAllowed([]events.AgentEventType{events.AgentEventAll}, ev))
	require.True(t, eventAllowed([]events.AgentEventType{ev.Type()}, ev))
	require.False(t, eventAllowed([]events.AgentEventType{events.AgentEventTypeCustom}, ev))
}

func TestEmitToolComplete(t *testing.T) {
	var got []events.AgentEvent
	emitToolComplete(func(ev events.AgentEvent) { got = append(got, ev) }, "mid", "tc1", "result")
	require.Len(t, got, 2)
	require.Equal(t, events.AgentEventTypeToolCallEnd, got[0].Type())
	require.Equal(t, events.AgentEventTypeToolCallResult, got[1].Type())
}

func TestPolicyToRunOpts(t *testing.T) {
	opts := policyToRunOpts("step", sdkruntime.ExecutionPolicy{
		MaxAttempts: 3,
		Timeout:     time.Second,
		Retry: sdkruntime.RetryPolicy{
			InitialInterval:    10 * time.Millisecond,
			MaximumInterval:    time.Second,
			BackoffCoefficient: 2,
		},
	})
	require.NotEmpty(t, opts)
	require.Len(t, policyToRunOpts("n", sdkruntime.ExecutionPolicy{}), 1)
}

func expectRunExecutes(ctx *mocks.MockContext, optionCount int) *mock.Call {
	args := make([]any, 0, 2+optionCount)
	args = append(args, mock.Anything, mock.Anything)
	for i := 0; i < optionCount; i++ {
		args = append(args, mock.Anything)
	}
	return ctx.On("Run", args...).Run(func(a mock.Arguments) {
		fnVal := reflect.ValueOf(a.Get(0))
		outPtr := reflect.ValueOf(a.Get(1))
		results := fnVal.Call([]reflect.Value{reflect.ValueOf(ctx)})
		if !results[1].IsNil() {
			return
		}
		val := results[0]
		if val.Kind() == reflect.Interface && !val.IsNil() {
			val = val.Elem()
		}
		if !val.IsValid() || !outPtr.IsValid() || outPtr.IsNil() {
			return
		}
		dest := outPtr.Elem()
		if dest.CanSet() && val.Type().AssignableTo(dest.Type()) {
			dest.Set(val)
		}
	}).Return(nil)
}

type seqLLM struct {
	responses []*interfaces.LLMResponse
	i         int
}

func (c *seqLLM) Generate(_ context.Context, _ *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	if c.i >= len(c.responses) {
		return &interfaces.LLMResponse{Content: "fallback"}, nil
	}
	r := c.responses[c.i]
	c.i++
	return r, nil
}
func (c *seqLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, nil
}
func (c *seqLLM) GetModel() string                    { return "seq" }
func (c *seqLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (c *seqLLM) IsStreamSupported() bool             { return false }

func TestExecuteAgentLoop_SimpleText(t *testing.T) {
	rt := testRestateRuntime("loop-agent")
	rt.AgentConfig.Limits.MaxIterations = 3
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{Content: "hello world"}}}

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once() // message-id: name + maxAttempts
	expectRunExecutes(ctx, 6).Once() // llm: full default policy

	result, err := rt.executeAgentLoop(restatesdk.WithMockContext(ctx), AgentLoopInput{
		agentLoopCore: agentLoopCore{RunID: "run-1", UserPrompt: "hi"},
	})
	require.NoError(t, err)
	require.Equal(t, "hello world", result.Content)
}

func TestEmitEvent_Filtered(t *testing.T) {
	rt := testRestateRuntime("a")
	ctx := mocks.NewMockContext(t)
	rt.emitEvent(restatesdk.WithMockContext(ctx), rt.eventLogServiceName, "", []events.AgentEventType{events.AgentEventAll},
		events.NewAgentTextMessageContentEvent("m", "x"))
	rt.emitEvent(restatesdk.WithMockContext(ctx), rt.eventLogServiceName, "topic", nil,
		events.NewAgentTextMessageContentEvent("m", "x"))
}

func TestEmitEvent_Publishes(t *testing.T) {
	rt := testRestateRuntime("a")
	ctx := mocks.NewMockContext(t)
	client := ctx.EXPECT().MockObjectClient(rt.eventLogServiceName, "run-1", "Publish")
	client.RequestAndReturn(mock.Anything, restatesdk.Void{}, nil)
	rt.emitEvent(restatesdk.WithMockContext(ctx), rt.eventLogServiceName, "run-1",
		[]events.AgentEventType{events.AgentEventAll},
		events.NewAgentTextMessageContentEvent("m", "hi"))
}

func TestEmitEvent_PublishesToParentEventLog(t *testing.T) {
	rt := testRestateRuntime("math")
	ctx := mocks.NewMockContext(t)
	parentLog := "AgentEventLog_main-agent"
	client := ctx.EXPECT().MockObjectClient(parentLog, "parent-run", "Publish")
	client.RequestAndReturn(mock.Anything, restatesdk.Void{}, nil)
	rt.emitEvent(restatesdk.WithMockContext(ctx), parentLog, "parent-run",
		[]events.AgentEventType{events.AgentEventAll},
		events.NewAgentTextMessageContentEvent("m", "hi"))
}

func TestEmitEventIngress_NoClient(t *testing.T) {
	rt := testRestateRuntime("a")
	rt.emitEventIngress(context.Background(), rt.eventLogServiceName, "topic", []events.AgentEventType{events.AgentEventAll},
		events.NewAgentTextMessageContentEvent("m", "x"))
}

func TestExecuteWithPolicy(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	expectRunExecutes(ctx, 1).Once()
	out, err := executeWithPolicy(restatesdk.WithMockContext(ctx), "ok", sdkruntime.ExecutionPolicy{},
		func(restatesdk.RunContext) (int, error) { return 7, nil })
	require.NoError(t, err)
	require.Equal(t, 7, out)

	ctx2 := mocks.NewMockContext(t)
	ctx2.On("Run", mock.Anything, mock.Anything, mock.Anything).Return(restatesdk.TerminalErrorf("nope"))
	_, err = executeWithPolicy(restatesdk.WithMockContext(ctx2), "boom", sdkruntime.ExecutionPolicy{},
		func(restatesdk.RunContext) (int, error) { return 0, nil })
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestExecuteWithPolicyErr(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	expectRunExecutes(ctx, 1).Once()
	require.NoError(t, executeWithPolicyErr(restatesdk.WithMockContext(ctx), "void", sdkruntime.ExecutionPolicy{},
		func(restatesdk.RunContext) error { return nil }))
}

func TestExecuteSingleTool_Unauthorized(t *testing.T) {
	rt := testRestateRuntime("a")
	ctx := mocks.NewMockContext(t)
	expectRunExecutes(ctx, 6).Once() // tool-auth default policy
	res, err := rt.executeSingleTool(restatesdk.WithMockContext(ctx), AgentLoopInput{agentLoopCore: agentLoopCore{RunID: "r"}}, "mid", 0,
		base.ToolCallRequest{ToolCallID: "tc", ToolName: "missing", ToolKind: types.ToolKindNative},
		sdkruntime.ResolveExecutionPolicies(sdkruntime.ExecutionConfigs{}), func(events.AgentEvent) {})
	require.NoError(t, err)
	require.Contains(t, res.message.Content, msgToolUnauthorized)
}

func TestExecuteToolsParallel_Empty(t *testing.T) {
	rt := testRestateRuntime("a")
	out, err := rt.executeToolsParallel(restatesdk.WithMockContext(mocks.NewMockContext(t)),
		AgentLoopInput{}, "m", 0, nil,
		sdkruntime.ResolveExecutionPolicies(sdkruntime.ExecutionConfigs{}), func(events.AgentEvent) {})
	require.NoError(t, err)
	require.Nil(t, out)
}

func TestMemoryScopeSet(t *testing.T) {
	if isMemoryScopeSet(interfaces.MemoryScope{}) {
		t.Fatal("empty should be unset")
	}
	if !isMemoryScopeSet(interfaces.MemoryScope{UserID: "u"}) {
		t.Fatal("UserID")
	}
	if !isMemoryScopeSet(interfaces.MemoryScope{TenantID: "t"}) {
		t.Fatal("TenantID")
	}
	if !isMemoryScopeSet(interfaces.MemoryScope{AgentID: "a"}) {
		t.Fatal("AgentID")
	}
	if !isMemoryScopeSet(interfaces.MemoryScope{Tags: map[string]string{"k": "v"}}) {
		t.Fatal("Tags")
	}
}

func TestAgentLoopRequest_JSONRoundTrip(t *testing.T) {
	in := AgentLoopRequest{
		agentLoopCore: agentLoopCore{
			RunID:            "run-1",
			UserPrompt:       "hi",
			EventTopic:       "run-1",
			EventTypes:       []events.AgentEventType{events.AgentEventAll},
			MaxSubAgentDepth: 2,
			SubAgentDepth:    1,
			EventLogService:  "AgentEventLog_Root",
			SubAgentRoutes: map[string]SubAgentRoute{
				"subagent_Child": {Name: "Child", ToolName: "subagent_Child", ServiceName: "AgentLoop_Child"},
			},
			MemoryScope: interfaces.MemoryScope{UserID: "u1"},
		},
		AgentName:        "Root",
		LLMStreamEnabled: true,
		StreamHandler:    true,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out AgentLoopRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.RunID != in.RunID || out.AgentName != in.AgentName || out.EventTopic != in.EventTopic {
		t.Fatalf("got %#v", out)
	}
	if out.EventLogService != "AgentEventLog_Root" {
		t.Fatalf("event log service: %#v", out)
	}
	if out.SubAgentDepth != 1 || out.MaxSubAgentDepth != 2 {
		t.Fatalf("depth: %#v", out)
	}
	if out.SubAgentRoutes["subagent_Child"].Name != "Child" || out.SubAgentRoutes["subagent_Child"].ServiceName != "AgentLoop_Child" {
		t.Fatalf("routes: %#v", out.SubAgentRoutes)
	}
	if out.MemoryScope.UserID != "u1" {
		t.Fatalf("memory: %#v", out.MemoryScope)
	}
}

func TestCancelRequest_JSON(t *testing.T) {
	b, err := json.Marshal(CancelRequest{RunID: "r", InvocationID: "inv"})
	if err != nil {
		t.Fatal(err)
	}
	var got CancelRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.InvocationID != "inv" || got.RunID != "r" {
		t.Fatalf("%#v", got)
	}
}

func TestAgentLoop_Cancel(t *testing.T) {
	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().CancelInvocation("inv_abc").Once()
	require.NoError(t, (AgentLoop{}).Cancel(restatesdk.WithMockContext(ctx), CancelRequest{InvocationID: "inv_abc"}))

	err := (AgentLoop{}).Cancel(restatesdk.WithMockContext(mocks.NewMockContext(t)), CancelRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invocation_id")
}

func TestAgentLoop_NilRuntime(t *testing.T) {
	_, err := (AgentLoop{}).Run(restatesdk.WithMockContext(mocks.NewMockContext(t)), AgentLoopRequest{
		agentLoopCore: agentLoopCore{RunID: "r"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no runtime")
}

func TestHandle_Simple(t *testing.T) {
	rt := testRestateRuntime("svc-agent")
	rt.AgentConfig.Limits.MaxIterations = 2
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{Content: "from-loop"}}}
	rt.tools.stash.Store("run-svc", stagedRun{})

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()
	// Run without EventTypes / approval: no AgentEventLog publishes, so no Clear.
	resp, err := (AgentLoop{rt: rt}).Run(restatesdk.WithMockContext(ctx), AgentLoopRequest{
		agentLoopCore: agentLoopCore{RunID: "run-svc", UserPrompt: "hi", EventTopic: "run-svc"},
	})
	require.NoError(t, err)
	require.Equal(t, "from-loop", resp.Result.Content)
	_, still := rt.tools.stash.Load("run-svc")
	require.False(t, still)
}

func TestHandle_RunSchedulesDelayedClear(t *testing.T) {
	rt := testRestateRuntime("svc-agent")
	rt.AgentConfig.Limits.MaxIterations = 2
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{Content: "approved-path"}}}
	rt.tools.stash.Store("run-appr", stagedRun{eventTypes: []events.AgentEventType{events.AgentEventTypeCustom}})

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()
	sendClient := mocks.NewMockClient(t)
	ctx.EXPECT().Object(rt.eventLogServiceName, "run-appr", "Clear").Return(sendClient).Once()
	sendClient.On("Send", mock.Anything, mock.Anything).Return(mocks.NewMockInvocation(t)).Once()

	resp, err := (AgentLoop{rt: rt}).Run(restatesdk.WithMockContext(ctx), AgentLoopRequest{
		agentLoopCore: agentLoopCore{
			RunID: "run-appr", UserPrompt: "hi", EventTopic: "run-appr",
			EventTypes: []events.AgentEventType{events.AgentEventTypeCustom},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "approved-path", resp.Result.Content)
}

func TestHandle_DisableClearSkipsClear(t *testing.T) {
	rt := testRestateRuntime("svc-agent")
	rt.config.EventLog.DisableClear = true
	rt.AgentConfig.Limits.MaxIterations = 2
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{Content: "no-clear"}}}
	rt.tools.stash.Store("run-nc", stagedRun{eventTypes: []events.AgentEventType{events.AgentEventTypeCustom}})

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()
	// No Object(..., "Clear") expectation — cleanup disabled.

	resp, err := (AgentLoop{rt: rt}).Run(restatesdk.WithMockContext(ctx), AgentLoopRequest{
		agentLoopCore: agentLoopCore{
			RunID: "run-nc", UserPrompt: "hi", EventTopic: "run-nc",
			EventTypes: []events.AgentEventType{events.AgentEventTypeCustom},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "no-clear", resp.Result.Content)
}

func TestHandle_StreamAndUnknownDelegate(t *testing.T) {
	rt := testRestateRuntime("svc-agent")
	rt.AgentConfig.Limits.MaxIterations = 2
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{Content: "streamed"}}}

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()
	sendClient := mocks.NewMockClient(t)
	ctx.EXPECT().Object(rt.eventLogServiceName, "run-st", "Clear").Return(sendClient).Once()
	sendClient.On("Send", mock.Anything, mock.Anything).Return(mocks.NewMockInvocation(t)).Once()

	resp, err := (AgentLoop{rt: rt}).Stream(restatesdk.WithMockContext(ctx), AgentLoopRequest{
		agentLoopCore: agentLoopCore{RunID: "run-st", UserPrompt: "hi"},
	})
	require.NoError(t, err)
	require.Equal(t, "streamed", resp.Result.Content)

	_, err = (AgentLoop{rt: testRestateRuntime("root")}).Run(
		restatesdk.WithMockContext(mocks.NewMockContext(t)),
		AgentLoopRequest{agentLoopCore: agentLoopCore{RunID: "r"}, AgentName: "Missing"},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match this AgentLoop")
}

func TestHandle_BudgetStopRun_IsTerminal(t *testing.T) {
	rt := testRestateRuntime("budget-stop")
	rt.AgentConfig.Limits.MaxIterations = 3
	rt.AgentConfig.Limits.Budget = &types.BudgetConfig{
		MaxTokens:  100,
		OnExceeded: types.BudgetStopRun,
	}
	rt.AgentConfig.LLM.Client = &seqLLM{responses: []*interfaces.LLMResponse{{
		Content: "too much",
		Usage:   &interfaces.LLMUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 110},
	}}}
	rt.tools.stash.Store("run-1", stagedRun{})

	ctx := mocks.NewMockContext(t)
	ctx.EXPECT().Wrap(mock.Anything).Return(ctx).Maybe()
	expectRunExecutes(ctx, 2).Once()
	expectRunExecutes(ctx, 6).Once()

	_, err := (AgentLoop{rt: rt}).Run(restatesdk.WithMockContext(ctx), AgentLoopRequest{
		agentLoopCore: agentLoopCore{RunID: "run-1", UserPrompt: "hi"},
	})
	require.Error(t, err)
	require.True(t, restatesdk.IsTerminalError(err), "got: %v", err)
	require.ErrorIs(t, err, types.ErrBudgetExceeded)
	_, still := rt.tools.stash.Load("run-1")
	require.False(t, still)
}

func TestTerminalLoopError(t *testing.T) {
	require.Nil(t, terminalLoopError(nil))
	require.ErrorIs(t, terminalLoopError(types.ErrBudgetExceeded), types.ErrBudgetExceeded)
	require.True(t, restatesdk.IsTerminalError(terminalLoopError(types.ErrBudgetExceeded)))
	require.True(t, restatesdk.IsTerminalError(terminalLoopError(types.ErrBudgetApprovalUnavailable)))
	other := errors.New("llm timeout")
	require.Equal(t, other, terminalLoopError(other))
	already := restatesdk.ToTerminalError(types.ErrBudgetExceeded)
	require.Equal(t, already, terminalLoopError(already))
}
