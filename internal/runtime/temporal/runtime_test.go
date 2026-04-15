package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/nexus-rpc/sdk-go/nexus"
	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	temporalmocks "go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// noopTemporalWorker is a minimal [worker.Worker] for exercising Start/Stop/Close without a real Temporal server.
type noopTemporalWorker struct {
	stopped bool
}

func (noopTemporalWorker) RegisterWorkflow(interface{}) {}
func (noopTemporalWorker) RegisterWorkflowWithOptions(interface{}, workflow.RegisterOptions) {
}
func (noopTemporalWorker) RegisterDynamicWorkflow(interface{}, workflow.DynamicRegisterOptions) {
}
func (noopTemporalWorker) RegisterActivity(interface{})                                      {}
func (noopTemporalWorker) RegisterActivityWithOptions(interface{}, activity.RegisterOptions) {}
func (noopTemporalWorker) RegisterDynamicActivity(interface{}, activity.DynamicRegisterOptions) {
}
func (noopTemporalWorker) RegisterNexusService(*nexus.Service) {}

func (noopTemporalWorker) Start() error                 { return nil }
func (noopTemporalWorker) Run(<-chan interface{}) error { return nil }
func (n *noopTemporalWorker) Stop()                     { n.stopped = true }

var _ worker.Worker = (*noopTemporalWorker)(nil)

func TestGetEventTaskQueue(t *testing.T) {
	if got := getEventTaskQueue("my-queue"); got != "my-queue-events" {
		t.Errorf("getEventTaskQueue(%q) = %q, want my-queue-events", "my-queue", got)
	}
}

func TestAgentNameFromExecuteRequest(t *testing.T) {
	if agentNameFromExecuteRequest(nil) != "" {
		t.Fatal("nil req")
	}
	if agentNameFromExecuteRequest(&sdkruntime.ExecuteRequest{}) != "" {
		t.Fatal("nil AgentSpec")
	}
	if got := agentNameFromExecuteRequest(&sdkruntime.ExecuteRequest{
		AgentSpec: &sdkruntime.AgentSpec{Name: "n"},
	}); got != "n" {
		t.Fatalf("got %q", got)
	}
}

func TestSyntheticStreamCompleteEvent(t *testing.T) {
	ev := syntheticStreamCompleteEvent(nil, "root")
	if ev == nil || ev.Type != types.AgentEventComplete || ev.AgentName != "root" {
		t.Fatalf("nil resp: %+v", ev)
	}

	ev2 := syntheticStreamCompleteEvent(&types.AgentResponse{
		Content:   "body",
		AgentName: "from-resp",
		Usage:     &types.LLMUsage{TotalTokens: 9},
	}, "ignored")
	if ev2.Content != "body" || ev2.AgentName != "from-resp" || ev2.Usage.TotalTokens != 9 {
		t.Fatalf("with AgentName: %+v", ev2)
	}

	ev3 := syntheticStreamCompleteEvent(&types.AgentResponse{Content: "c", AgentName: ""}, "fallback")
	if ev3.AgentName != "fallback" {
		t.Fatalf("fallback name: got %q", ev3.AgentName)
	}

	ev4 := syntheticStreamCompleteEvent(&types.AgentResponse{Content: "only"}, "")
	if ev4.AgentName != "" {
		t.Fatalf("empty rootName with empty resp.AgentName: got %q", ev4.AgentName)
	}
}

func TestResolveEventPipeline(t *testing.T) {
	l := logger.NoopLogger()
	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			logger:    l,
			taskQueue: "tq",
		},
	}
	ewf, etq, err := rt.resolveEventPipeline(context.Background(), "My Agent")
	if err != nil {
		t.Fatal(err)
	}
	if etq != "tq-events" {
		t.Fatalf("event task queue = %q", etq)
	}
	if !strings.HasPrefix(ewf, "agent-event-My-Agent-") {
		t.Fatalf("event workflow id = %q", ewf)
	}
	ewf2, etq2, err := rt.resolveEventPipeline(context.Background(), "My Agent")
	if err != nil {
		t.Fatal(err)
	}
	if ewf2 != ewf || etq2 != etq {
		t.Fatalf("second call same agent should match: %q/%q vs %q/%q", ewf, etq, ewf2, etq2)
	}
	if rt.activeEventWorkflowID == "" {
		t.Fatal("activeEventWorkflowID should be set")
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
	// Spaces/special chars sanitized like event workflow IDs.
	if got := rt.getWorkflowID("  my agent  ", false); !strings.HasPrefix(got, "agent-run-my-agent-") {
		t.Fatalf("sanitize run id: %q", got)
	}
}

func TestGetEventWorkflowID_Format(t *testing.T) {
	rt := &TemporalRuntime{}
	id := rt.getEventWorkflowID("AgentX")
	if !strings.HasPrefix(id, "agent-event-AgentX-") || len(id) < len("agent-event-AgentX-")+8 {
		t.Fatalf("unexpected event workflow id: %q", id)
	}
	id2 := rt.getEventWorkflowID("AgentX")
	if id2 != id {
		t.Fatalf("expected stable id for same runtime+name: %q vs %q", id, id2)
	}
	other := (&TemporalRuntime{}).getEventWorkflowID("AgentX")
	if other == id {
		t.Fatalf("different TemporalRuntime should not share event workflow id: %q", id)
	}
	// Same runtime: sanitized name shares the per-runtime suffix.
	wantHello := fmt.Sprintf("agent-event-hello-world-%s", rt.eventWorkflowIDSuffix)
	if got := rt.getEventWorkflowID("  hello world  "); got != wantHello {
		t.Fatalf("sanitize: got %q want %q", got, wantHello)
	}
}

func TestSanitizeTemporalWorkflowIDSegment_maxLength(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := sanitizeTemporalWorkflowIDSegment(long)
	if len(got) != maxAgentNameWorkflowSegmentBytes {
		t.Fatalf("len=%d want %d", len(got), maxAgentNameWorkflowSegmentBytes)
	}
}

func TestTruncateUTF8String(t *testing.T) {
	// "hello" (5) + U+65E5 日 (3 UTF-8 bytes) = 8 bytes total.
	s := "hello" + "\u65e5"
	if truncateUTF8String(s, 8) != s {
		t.Fatalf("8-byte cap should keep full string")
	}
	if got := truncateUTF8String(s, 7); got != "hello" {
		t.Fatalf("7-byte cap must not split 日; got %q", got)
	}
	if !utf8.ValidString(truncateUTF8String(s, 6)) {
		t.Fatal("result must be valid UTF-8")
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

func TestTemporalRuntime_Start_Idempotent(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.agentWorker = &noopTemporalWorker{}

	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start second call: %v", err)
	}
}

func TestTemporalRuntime_Stop_RemoteOwnedClient(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("Close").Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithRemoteWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.ownsTemporalClient = true
	aw := &noopTemporalWorker{}
	rt.agentWorker = aw

	rt.Stop()
	if !aw.stopped {
		t.Fatal("expected agent worker Stop when remoteWorker is set")
	}
	tc.AssertExpectations(t)
}

func TestTemporalRuntime_Stop_RemoteOwnedClientNoAgentWorker(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("Close").Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithRemoteWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.ownsTemporalClient = true
	rt.agentWorker = nil

	rt.Stop()
	tc.AssertExpectations(t)
}

func TestTemporalRuntime_Stop_LocalEmbed(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithRemoteWorker(false),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.Stop()
}

func TestTemporalRuntime_Close_Minimal(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()
}

func TestTemporalRuntime_Close_OwnsTemporalClient(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("Close").Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.ownsTemporalClient = true
	rt.Close()
	tc.AssertExpectations(t)
}

func TestTemporalRuntime_Close_StopsWorkers(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ew := &noopTemporalWorker{}
	aw := &noopTemporalWorker{}
	rt.eventWorker = ew
	rt.agentWorker = aw

	rt.Close()
	if !ew.stopped || !aw.stopped {
		t.Fatalf("event stopped=%v agent stopped=%v", ew.stopped, aw.stopped)
	}
}

func TestTemporalRuntime_Close_ActiveWorkflows(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	wfRun := temporalmocks.NewWorkflowRun(t)

	tc.On("TerminateWorkflow", mock.Anything, "run-w1", "", "agent closed").Return(nil).Once()
	tc.On("SignalWorkflow", mock.Anything, "evt-w1", "", eventWorkflowCompleteSignal, nil).Return(nil).Once()
	tc.On("GetWorkflow", mock.Anything, "evt-w1", "").Return(wfRun).Once()
	wfRun.On("Get", mock.Anything, nil).Return(nil).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.runMu.Lock()
	rt.activeRunWorkflowID = "run-w1"
	rt.activeEventWorkflowID = "evt-w1"
	rt.runMu.Unlock()

	rt.Close()
	tc.AssertExpectations(t)
	wfRun.AssertExpectations(t)
}
