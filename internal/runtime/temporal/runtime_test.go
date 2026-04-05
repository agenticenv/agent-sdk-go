package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	temporalmocks "go.temporal.io/sdk/mocks"
)

func TestGetEventTaskQueue(t *testing.T) {
	if got := getEventTaskQueue("my-queue"); got != "my-queue-events" {
		t.Errorf("getEventTaskQueue(%q) = %q, want my-queue-events", "my-queue", got)
	}
}

func TestStreamCompleteEndsRun(t *testing.T) {
	root := "Main agent"
	if streamCompleteEndsRun(nil, root) {
		t.Error("nil event should not end run")
	}
	if streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventContent}, root) {
		t.Error("non-complete should not end run")
	}
	if !streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventComplete, AgentName: ""}, root) {
		t.Error("complete with empty agent should end run (legacy)")
	}
	if !streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventComplete, AgentName: root}, root) {
		t.Error("complete from root should end run")
	}
	if streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventComplete, AgentName: "MathSpecialist"}, root) {
		t.Error("complete from sub-agent should not end root run")
	}
	// Trimming: spaced names should still match root
	if !streamCompleteEndsRun(&types.AgentEvent{Type: types.AgentEventComplete, AgentName: "  Main agent  "}, " Main agent ") {
		t.Error("complete should match after trim")
	}
}

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

func TestAgent_BeginRunEndRun(t *testing.T) {
	l := logger.DefaultLogger("error")
	a := &TemporalRuntime{TemporalRuntimeConfig: TemporalRuntimeConfig{logger: l}}

	cleanup, err := a.beginRun("wf1")
	if err != nil {
		t.Fatalf("beginRun: %v", err)
	}
	cleanup()

	cleanup2, err := a.beginRun("wf1")
	if err != nil {
		t.Fatalf("beginRun after cleanup: %v", err)
	}
	defer cleanup2()

	_, err = a.beginRun("wf2")
	if err != ErrAgentAlreadyRunning {
		t.Errorf("beginRun concurrent: got %v, want ErrAgentAlreadyRunning", err)
	}
}

func TestEventChannelName(t *testing.T) {
	if got := eventChannelName("run-abc"); got != "agent_event_run-abc" {
		t.Errorf("eventChannelName = %q", got)
	}
}

func TestRetryPolicy(t *testing.T) {
	p := retryPolicy(7)
	if p == nil || p.MaximumAttempts != 7 {
		t.Fatalf("retryPolicy(7) = %+v", p)
	}
}

func TestApplyLLMSampling(t *testing.T) {
	req := &interfaces.LLMRequest{}
	applyLLMSampling(nil, req)
	if req.Temperature != nil || req.MaxTokens != 0 {
		t.Error("nil sampling should not modify request")
	}
	temp := 0.3
	topP := 0.9
	topK := 5
	applyLLMSampling(&types.LLMSampling{
		Temperature: &temp,
		MaxTokens:   42,
		TopP:        &topP,
		TopK:        &topK,
	}, req)
	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Errorf("Temperature = %v", req.Temperature)
	}
	if req.MaxTokens != 42 {
		t.Errorf("MaxTokens = %d", req.MaxTokens)
	}
	if req.TopP == nil || *req.TopP != 0.9 {
		t.Errorf("TopP = %v", req.TopP)
	}
	if req.TopK == nil || *req.TopK != 5 {
		t.Errorf("TopK = %v", req.TopK)
	}
}

func TestApplyLLMSampling_reasoning(t *testing.T) {
	req := &interfaces.LLMRequest{}
	applyLLMSampling(&types.LLMSampling{
		Reasoning: &interfaces.LLMReasoning{
			Enabled:      true,
			Effort:       "medium",
			BudgetTokens: 2048,
		},
	}, req)
	if req.Reasoning == nil {
		t.Fatal("expected Reasoning")
	}
	if req.Reasoning.Effort != "medium" {
		t.Errorf("Effort = %q", req.Reasoning.Effort)
	}
	if req.Reasoning.BudgetTokens != 2048 {
		t.Errorf("BudgetTokens = %d", req.Reasoning.BudgetTokens)
	}
	if !req.Reasoning.Enabled {
		t.Error("expected Enabled")
	}
}

func TestKeyvalsToAny(t *testing.T) {
	kv := []interface{}{"k", 1}
	out := keyvalsToAny(kv)
	if len(out) != 2 || out[0] != "k" || out[1] != 1 {
		t.Fatalf("keyvalsToAny = %v", out)
	}
}

func TestTemporalRuntime_SetEventBus_GetEventBus(t *testing.T) {
	l := logger.NoopLogger()
	rt := &TemporalRuntime{TemporalRuntimeConfig: TemporalRuntimeConfig{logger: l}}
	if rt.GetEventBus() != nil {
		t.Fatal("zero-value runtime should have nil event bus until set")
	}
	bus := eventbus.NewInmem(l)
	rt.SetEventBus(bus)
	if rt.GetEventBus() != bus {
		t.Fatal("GetEventBus should return the bus set by SetEventBus")
	}
}

func TestGetWorkflowID_Format(t *testing.T) {
	rt := &TemporalRuntime{}
	run := rt.getWorkflowID("MyAgent", false)
	if !strings.HasPrefix(run, "agent-run-MyAgent-") || len(run) < len("agent-run-MyAgent-")+8 {
		t.Fatalf("unexpected run id: %q", run)
	}
	stream := rt.getWorkflowID("Helper", true)
	if !strings.HasPrefix(stream, "agent-stream-Helper-") {
		t.Fatalf("unexpected stream id: %q", stream)
	}
}

func TestGetEventWorkflowID_Format(t *testing.T) {
	rt := &TemporalRuntime{}
	id := rt.getEventWorkflowID("AgentX")
	if !strings.HasPrefix(id, "agent-event-AgentX-") {
		t.Fatalf("unexpected event workflow id: %q", id)
	}
}

func TestApprove_InvalidStatus(t *testing.T) {
	rt := &TemporalRuntime{}
	err := rt.Approve(context.Background(), "dGVzdA==", types.ApprovalStatusPending)
	if err == nil || !strings.Contains(err.Error(), "invalid approval status") {
		t.Fatalf("Approve = %v", err)
	}
}

func TestApprove_InvalidToken(t *testing.T) {
	rt := &TemporalRuntime{}
	err := rt.Approve(context.Background(), "not-valid-base64!!!", types.ApprovalStatusApproved)
	if err == nil || !strings.Contains(err.Error(), "invalid approval token") {
		t.Fatalf("Approve = %v", err)
	}
}

func TestOnApproval_InvalidStatus(t *testing.T) {
	rt := &TemporalRuntime{}
	err := rt.OnApproval(context.Background(), "dGVzdA==", types.ApprovalStatusNone)
	if err == nil || !strings.Contains(err.Error(), "invalid approval status") {
		t.Fatalf("OnApproval = %v", err)
	}
}

func TestOnApproval_InvalidToken(t *testing.T) {
	rt := &TemporalRuntime{}
	err := rt.OnApproval(context.Background(), "###", types.ApprovalStatusRejected)
	if err == nil || !strings.Contains(err.Error(), "invalid approval token") {
		t.Fatalf("OnApproval = %v", err)
	}
}

func describeTaskQueueWithPollers() *workflowservice.DescribeTaskQueueResponse {
	return &workflowservice.DescribeTaskQueueResponse{
		Pollers: []*taskqueuepb.PollerInfo{{}},
	}
}

func TestTemporalRuntime_Run_Success(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	wfRun := temporalmocks.NewWorkflowRun(t)

	want := &types.AgentResponse{AgentName: "agent-a", Content: "hello", Model: "m"}
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(wfRun, nil)
	wfRun.On("Get", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(1).(**types.AgentResponse)
		if p != nil {
			*p = want
		}
	}).Return(nil)

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := rt.Execute(context.Background(), &sdkruntime.ExecuteRequest{UserPrompt: "hi", AgentSpec: &sdkruntime.AgentSpec{Name: "agent-a"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.AgentName != want.AgentName || resp.Content != want.Content || resp.Model != want.Model {
		t.Fatalf("resp = %+v, want %+v", resp, want)
	}
}

func TestTemporalRuntime_Run_NoWorkers(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(&workflowservice.DescribeTaskQueueResponse{Pollers: nil}, nil)

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = rt.Execute(ctx, &sdkruntime.ExecuteRequest{UserPrompt: "hi", AgentSpec: &sdkruntime.AgentSpec{Name: "agent-a"}})
	if err == nil {
		t.Fatal("expected error when no workers")
	}
	if !strings.Contains(err.Error(), "no workers available") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestTemporalRuntime_Run_ExecuteWorkflowError(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("start failed"))

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.Execute(context.Background(), &sdkruntime.ExecuteRequest{UserPrompt: "hi", AgentSpec: &sdkruntime.AgentSpec{Name: "agent-a"}})
	if err == nil || err.Error() != "start failed" {
		t.Fatalf("got %v, want start failed", err)
	}
}

func TestTemporalRuntime_Run_WorkflowGetError(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	wfRun := temporalmocks.NewWorkflowRun(t)
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(wfRun, nil)
	wfRun.On("Get", mock.Anything, mock.Anything).Return(errors.New("workflow failed"))

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.Execute(context.Background(), &sdkruntime.ExecuteRequest{UserPrompt: "hi", AgentSpec: &sdkruntime.AgentSpec{Name: "agent-a"}})
	if err == nil || err.Error() != "workflow failed" {
		t.Fatalf("got %v, want workflow failed", err)
	}
}

func TestTemporalRuntime_ExecuteStream_Success(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	// Do not use NewWorkflowRun(t): ExecuteStream forwards AgentEventComplete on outCh before
	// blocking on getWG.Wait(), so the test can finish before the Get goroutine runs; auto
	// AssertExpectations in NewWorkflowRun's cleanup would race and flake.
	wfRun := &temporalmocks.WorkflowRun{}

	var localChannel string
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			in := args.Get(3).(AgentWorkflowInput)
			localChannel = in.LocalChannelName
		}).Return(wfRun, nil)
	wfRun.On("Get", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(1).(**types.AgentResponse)
		if p != nil {
			*p = &types.AgentResponse{AgentName: "root"}
		}
	}).Return(nil)

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "root"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	outCh, err := rt.ExecuteStream(ctx, &sdkruntime.ExecuteRequest{UserPrompt: "hi", AgentSpec: &sdkruntime.AgentSpec{Name: "root"}})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if localChannel == "" {
		t.Fatal("expected workflow input to set local channel")
	}

	payload, err := json.Marshal(&types.AgentEvent{
		Type:      types.AgentEventComplete,
		AgentName: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.GetEventBus().Publish(ctx, localChannel, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var sawComplete bool
	for ev := range outCh {
		if ev != nil && ev.Type == types.AgentEventComplete {
			sawComplete = true
			break
		}
	}
	if !sawComplete {
		t.Fatal("expected complete event on stream")
	}
}

func TestTemporalRuntime_ExecuteStream_WorkflowGetError(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	wfRun := temporalmocks.NewWorkflowRun(t)
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(wfRun, nil)
	wfRun.On("Get", mock.Anything, mock.Anything).Return(errors.New("stream wf err"))

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "root"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	outCh, err := rt.ExecuteStream(context.Background(), &sdkruntime.ExecuteRequest{UserPrompt: "hi", AgentSpec: &sdkruntime.AgentSpec{Name: "root"}})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	var sawErr bool
	for ev := range outCh {
		if ev != nil && ev.Type == types.AgentEventError && ev.Content == "stream wf err" {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Fatal("expected error event on stream")
	}
}
