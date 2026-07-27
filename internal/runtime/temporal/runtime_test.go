package temporal

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/nexus-rpc/sdk-go/nexus"
	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
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

func TestAgentNameFromRuntime(t *testing.T) {
	if agentNameFromRuntime(nil) != "" {
		t.Fatal("nil rt")
	}
	rt := &TemporalRuntime{
		Runtime: base.Runtime{AgentSpec: sdkruntime.AgentSpec{Name: "n"}},
	}
	if got := agentNameFromRuntime(rt); got != "n" {
		t.Fatalf("got %q", got)
	}
}

func TestSubAgentQuery(t *testing.T) {
	if base.SubAgentQuery(nil) != "" {
		t.Error("nil args")
	}
	if base.SubAgentQuery(map[string]any{}) != "" {
		t.Error("empty map")
	}
	if got := base.SubAgentQuery(map[string]any{"query": "hello"}); got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestAgent_BeginRunEndRun(t *testing.T) {
	l := logger.DefaultLogger("error")
	a := &TemporalRuntime{
		logger:     l,
		activeRuns: store.NewKV[string, *runHandle](),
	}
	h := &runHandle{}

	// First run: begin then end, map should be empty after cleanup.
	a.activeRuns.Set("wf1", h)
	if _, ok := a.activeRuns.Get("wf1"); !ok {
		t.Error("wf1 should be in active set after beginRun")
	}
	a.activeRuns.Delete("wf1")
	if _, ok := a.activeRuns.Get("wf1"); ok {
		t.Error("wf1 should be removed from active set after cleanup")
	}

	// Concurrent runs: two distinct workflow IDs are both tracked simultaneously.
	a.activeRuns.Set("wf2", h)
	a.activeRuns.Set("wf3", h)
	if _, ok := a.activeRuns.Get("wf2"); !ok {
		t.Error("wf2 should be in active set")
	}
	if _, ok := a.activeRuns.Get("wf3"); !ok {
		t.Error("wf3 should be in active set")
	}

	// Ending one run does not affect the other.
	a.activeRuns.Delete("wf2")
	if _, ok := a.activeRuns.Get("wf2"); ok {
		t.Error("wf2 should be removed after its cleanup")
	}
	if _, ok := a.activeRuns.Get("wf3"); !ok {
		t.Error("wf3 should still be in active set")
	}
	a.activeRuns.Delete("wf3")
}

func TestRetryPolicy(t *testing.T) {
	p := retryPolicy(7)
	if p == nil || p.MaximumAttempts != 7 {
		t.Fatalf("retryPolicy(7) = %+v", p)
	}
}

func TestKeyvalsToAny(t *testing.T) {
	kv := []interface{}{"k", 1}
	out := keyvalsToAny(kv)
	if len(out) != 2 || out[0] != "k" || out[1] != 1 {
		t.Fatalf("keyvalsToAny = %v", out)
	}
}

func TestGetWorkflowID_Format(t *testing.T) {
	rt := &TemporalRuntime{}
	run := rt.getWorkflowID("runID", "MyAgent", false)
	if !strings.HasPrefix(run, "agent-run-MyAgent-") || len(run) < len("agent-run-MyAgent-") {
		t.Fatalf("unexpected run id: %q", run)
	}
	stream := rt.getWorkflowID("runID", "Helper", true)
	if !strings.HasPrefix(stream, "agent-stream-Helper-") {
		t.Fatalf("unexpected stream id: %q", stream)
	}
	// Spaces/special chars sanitized like event workflow IDs.
	if got := rt.getWorkflowID("runID", "  my agent  ", false); !strings.HasPrefix(got, "agent-run-my-agent-") {
		t.Fatalf("sanitize run id: %q", got)
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
	err := rt.approve(context.Background(), "dGVzdA==", types.ApprovalStatusPending)
	if err == nil || !strings.Contains(err.Error(), "invalid approval status") {
		t.Fatalf("approve = %v", err)
	}
}

func TestApprove_InvalidToken(t *testing.T) {
	rt := &TemporalRuntime{}
	err := rt.approve(context.Background(), "not-valid-base64!!!", types.ApprovalStatusApproved)
	if err == nil || !strings.Contains(err.Error(), "invalid approval token") {
		t.Fatalf("approve = %v", err)
	}
}

func TestOnApproval_InvalidStatus(t *testing.T) {
	rt := &TemporalRuntime{}
	err := rt.approve(context.Background(), "dGVzdA==", types.ApprovalStatusNone)
	if err == nil || !strings.Contains(err.Error(), "invalid approval status") {
		t.Fatalf("approve = %v", err)
	}
}

func TestOnApproval_InvalidToken(t *testing.T) {
	rt := &TemporalRuntime{}
	err := rt.approve(context.Background(), "###", types.ApprovalStatusRejected)
	if err == nil || !strings.Contains(err.Error(), "invalid approval token") {
		t.Fatalf("approve = %v", err)
	}
}

func describeTaskQueueWithPollers() *workflowservice.DescribeTaskQueueResponse {
	return &workflowservice.DescribeTaskQueueResponse{
		Pollers: []*taskqueuepb.PollerInfo{{}},
	}
}

// describeWorkflowRunning returns a DescribeWorkflowExecution response indicating the workflow is still running.
// Used in unit tests to satisfy the pre-check inside Events() without a real Temporal server.
func describeWorkflowRunning() *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
	}
}

func TestTemporalRuntime_Run_Success(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	wfRun := temporalmocks.NewWorkflowRun(t)

	want := &types.AgentRunResult{AgentName: "agent-a", Content: "hello", Model: "m"}
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(wfRun, nil)
	tc.On("GetWorkflow", mock.Anything, mock.Anything, "").Return(wfRun).Maybe()
	tc.On("CancelWorkflow", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	wfRun.On("Get", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(1).(**types.AgentRunResult)
		if p != nil {
			*p = want
		}
	}).Return(nil)

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	resp, err := handle.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.AgentName != want.AgentName || resp.Content != want.Content || resp.Model != want.Model {
		t.Fatalf("resp = %+v, want %+v", resp, want)
	}
	if resp.RunID != handle.ID() {
		t.Fatalf("RunID = %q, want handle ID %q", resp.RunID, handle.ID())
	}
	waitHandleDone(t, handle.Done())
}

func TestTemporalRuntime_Run_NoWorkers(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(&workflowservice.DescribeTaskQueueResponse{Pollers: nil}, nil)

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = rt.Run(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
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
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
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
	tc.On("GetWorkflow", mock.Anything, mock.Anything, "").Return(wfRun).Maybe()
	tc.On("CancelWorkflow", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	wfRun.On("Get", mock.Anything, mock.Anything).Return(errors.New("workflow failed"))

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, err = handle.Get(context.Background())
	if err == nil || err.Error() != "workflow failed" {
		t.Fatalf("got %v, want workflow failed", err)
	}
	waitHandleDone(t, handle.Done())
}

func TestTemporalRuntime_Stream_Success(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	// Do not use NewWorkflowRun(t): avoids AssertExpectations flake on async Get.
	wfRun := &temporalmocks.WorkflowRun{}

	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(wfRun, nil)
	tc.On("GetWorkflow", mock.Anything, mock.Anything, "").Return(wfRun).Maybe()
	wfRun.On("Get", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(1).(**types.AgentRunResult)
		if p != nil {
			*p = &types.AgentRunResult{AgentName: "root"}
		}
	}).Return(nil)
	// Events gate (running), then Subscribe ends via failed poll + terminal describe.
	tc.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWorkflowRunning(), nil).Once()
	stubWorkflowStreamSubscribeEnd(tc)
	tc.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWorkflowCompleted(), nil).Maybe()
	tc.On("CancelWorkflow", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "root"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	h, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	outCh, err := h.Events(ctx, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var sawStarted, sawComplete bool
	for ev := range outCh {
		if ev == nil {
			continue
		}
		switch ev.Type() {
		case events.AgentEventTypeRunStarted:
			sawStarted = true
		case events.AgentEventTypeRunFinished:
			sawComplete = true
		}
	}
	if !sawStarted {
		t.Fatal("expected RUN_STARTED event on stream")
	}
	if !sawComplete {
		t.Fatal("expected RUN_FINISHED event on stream")
	}
	waitHandleDone(t, h.Done())
}

func TestTemporalRuntime_Stream_WorkflowGetError(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	// Do not use NewWorkflowRun(t): avoids AssertExpectations flake on async Get.
	wfRun := &temporalmocks.WorkflowRun{}
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(wfRun, nil)
	tc.On("GetWorkflow", mock.Anything, mock.Anything, "").Return(wfRun).Maybe()
	wfRun.On("Get", mock.Anything, mock.Anything).Return(errors.New("stream wf err"))
	tc.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWorkflowRunning(), nil).Once()
	stubWorkflowStreamSubscribeEnd(tc)
	tc.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWorkflowCompleted(), nil).Maybe()
	tc.On("CancelWorkflow", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "root"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	h, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	outCh, err := h.Events(ctx, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var sawErr bool
	for ev := range outCh {
		if ev != nil && ev.Type() == events.AgentEventTypeRunError {
			errEv, ok := ev.(*events.AgentRunErrorEvent)
			if ok && errEv.Message == "stream wf err" {
				sawErr = true
			}
			break
		}
	}
	if !sawErr {
		t.Fatal("expected RUN_ERROR event with correct message on stream")
	}
	waitHandleDone(t, h.Done())
}

func TestTemporalRuntime_Start_Idempotent(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
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
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
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
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
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
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
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
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
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
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
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
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	aw := &noopTemporalWorker{}
	rt.agentWorker = aw

	rt.Close()
	if !aw.stopped {
		t.Fatalf("agent worker should be stopped after Close")
	}
}

func TestStopWorkflow_TimeoutTerminatesImmediately(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	done := make(chan struct{})
	tc.On("TerminateWorkflow", mock.Anything, "wf-timeout", "", "run timeout").
		Run(func(mock.Arguments) { close(done) }).
		Return(nil).Once()

	rt := &TemporalRuntime{temporalClient: tc, logger: logger.NoopLogger()}
	rt.stopWorkflow(context.Background(), "wf-timeout", context.DeadlineExceeded, done)
	tc.AssertExpectations(t)
}

// TestStopWorkflow_CancelFallsBackToTerminate: Cancel RPC succeeds but Done never closes
// (no worker) → Terminate after the hardcoded 3s grace.
func TestStopWorkflow_CancelFallsBackToTerminate(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	done := make(chan struct{})
	tc.On("CancelWorkflow", mock.Anything, "wf-cancel", "").Return(nil).Once()
	tc.On("TerminateWorkflow", mock.Anything, "wf-cancel", "", "run cancelled").
		Run(func(mock.Arguments) { close(done) }).
		Return(nil).Once()

	rt := &TemporalRuntime{temporalClient: tc, logger: logger.NoopLogger()}
	start := time.Now()
	rt.stopWorkflow(context.Background(), "wf-cancel", context.Canceled, done)
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Fatalf("expected ~3s grace before terminate, finished in %v", elapsed)
	}
	tc.AssertExpectations(t)
}

func TestStopWorkflow_CancelCompletesWithoutTerminate(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	done := make(chan struct{})
	tc.On("CancelWorkflow", mock.Anything, "wf-soft", "").
		Run(func(mock.Arguments) { close(done) }).
		Return(nil).Once()

	rt := &TemporalRuntime{temporalClient: tc, logger: logger.NoopLogger()}
	rt.stopWorkflow(context.Background(), "wf-soft", context.Canceled, done)
	tc.AssertExpectations(t)
}

// TestTemporalRuntime_Close_ActiveRuns_TerminatesDirectly: Close sends TerminateWorkflow
// (not CancelWorkflow) for active runs so shutdown is immediate with no 3s grace period.
func TestTemporalRuntime_Close_ActiveRuns_TerminatesDirectly(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	done := make(chan struct{})
	tc.On("TerminateWorkflow", mock.Anything, "run-w1", "", "agent closed").
		Run(func(mock.Arguments) { close(done) }).
		Return(nil).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.activeRuns.Set("run-w1", &runHandle{doneCh: done})

	rt.Close()
	tc.AssertExpectations(t)
}

// TestTemporalRuntime_Close_ActiveStreams_TerminatesDirectly: Close sends TerminateWorkflow
// (not CancelWorkflow) for active streams, same direct-terminate path as runs.
func TestTemporalRuntime_Close_ActiveStreams_TerminatesDirectly(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	done := make(chan struct{})
	tc.On("TerminateWorkflow", mock.Anything, "stream-w1", "", "agent closed").
		Run(func(mock.Arguments) { close(done) }).
		Return(nil).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt.activeStreams.Set("stream-w1", &streamHandle{runHandle: &runHandle{doneCh: done}})

	rt.Close()
	tc.AssertExpectations(t)
}

func describeWorkflowCompleted() *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
		},
	}
}

func TestTemporalRuntime_GetRunHandle_Running(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "run-abc"
	workflowID := "agent-run-agent-a-" + runID

	release := make(chan struct{})
	want := &types.AgentRunResult{Content: "ok"}
	wfRun := blockingWorkflowRun(release, want, nil)

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(describeWorkflowRunning(), nil).Once()
	tc.On("GetWorkflow", mock.Anything, workflowID, "").Return(wfRun).Maybe()
	tc.On("CancelWorkflow", mock.Anything, workflowID, "").Return(nil).Maybe()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := rt.GetRunHandle(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if h.ID() != runID {
		t.Fatalf("ID: got %q want %q", h.ID(), runID)
	}

	// Drain await (GetWorkflow) so mock expectations are met before teardown.
	close(release)
	got, err := h.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Content != "ok" || got.RunID != runID {
		t.Fatalf("Get: %+v", got)
	}
	waitHandleDone(t, h.Done())
}

func TestTemporalRuntime_GetRunHandle_NotFound(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "missing"
	workflowID := "agent-run-agent-a-" + runID

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(nil, errors.New("workflow not found for ID")).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.GetRunHandle(context.Background(), runID)
	if !errors.Is(err, types.ErrRunNotFound) {
		t.Fatalf("got %v, want ErrRunNotFound", err)
	}
}

func TestTemporalRuntime_GetRunHandle_AlreadyCompleted(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "done-run"
	workflowID := "agent-run-agent-a-" + runID

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(describeWorkflowCompleted(), nil).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.GetRunHandle(context.Background(), runID)
	if !errors.Is(err, types.ErrRunAlreadyCompleted) {
		t.Fatalf("got %v, want ErrRunAlreadyCompleted", err)
	}
}

func TestTemporalRuntime_GetRunHandle_EmptyRunID(t *testing.T) {
	rt := &TemporalRuntime{logger: logger.NoopLogger()}
	_, err := rt.GetRunHandle(context.Background(), "  ")
	if !errors.Is(err, types.ErrRunNotFound) {
		t.Fatalf("got %v, want ErrRunNotFound", err)
	}
}

func TestTemporalRuntime_GetStreamHandle_Running(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "stream-abc"
	workflowID := "agent-stream-agent-a-" + runID

	release := make(chan struct{})
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok"}, nil)

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(describeWorkflowRunning(), nil).Once()
	tc.On("GetWorkflow", mock.Anything, workflowID, "").Return(wfRun).Maybe()
	tc.On("CancelWorkflow", mock.Anything, workflowID, "").Return(nil).Maybe()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := rt.GetStreamHandle(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if h.ID() != runID {
		t.Fatalf("ID: got %q want %q", h.ID(), runID)
	}
	t.Cleanup(func() {
		close(release)
		waitHandleDone(t, h.Done())
	})
}

func TestTemporalRuntime_GetStreamHandle_NotFound(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "missing"
	workflowID := "agent-stream-agent-a-" + runID

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(nil, errors.New("workflow not found for ID")).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.GetStreamHandle(context.Background(), runID)
	if !errors.Is(err, types.ErrStreamNotFound) {
		t.Fatalf("got %v, want ErrStreamNotFound", err)
	}
}

func TestTemporalRuntime_GetStreamHandle_AlreadyCompleted(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "done-stream"
	workflowID := "agent-stream-agent-a-" + runID

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(describeWorkflowCompleted(), nil).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.GetStreamHandle(context.Background(), runID)
	if !errors.Is(err, types.ErrRunAlreadyCompleted) {
		t.Fatalf("got %v, want ErrRunAlreadyCompleted", err)
	}
}

func TestTemporalRuntime_GetStreamHandle_EmptyRunID(t *testing.T) {
	rt := &TemporalRuntime{logger: logger.NoopLogger()}
	_, err := rt.GetStreamHandle(context.Background(), "  ")
	if !errors.Is(err, types.ErrStreamNotFound) {
		t.Fatalf("got %v, want ErrStreamNotFound", err)
	}
}

func TestTemporalRuntime_Run_NilRequest(t *testing.T) {
	rt := &TemporalRuntime{logger: logger.NoopLogger()}
	_, err := rt.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "nil RunRequest") {
		t.Fatalf("got %v, want nil RunRequest error", err)
	}
}

func TestTemporalRuntime_Stream_NilRequest(t *testing.T) {
	rt := &TemporalRuntime{logger: logger.NoopLogger()}
	_, err := rt.Stream(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "nil RunRequest") {
		t.Fatalf("got %v, want nil RunRequest error", err)
	}
}

func TestTemporalRuntime_Stream_NoWorkers(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(&workflowservice.DescribeTaskQueueResponse{Pollers: nil}, nil)

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err == nil {
		t.Fatal("expected error when no workers")
	}
	if !strings.Contains(err.Error(), "no workers available") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestTemporalRuntime_Stream_ExecuteWorkflowError(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	tc.On("DescribeTaskQueue", mock.Anything, "tq", enumspb.TASK_QUEUE_TYPE_WORKFLOW).
		Return(describeTaskQueueWithPollers(), nil)
	tc.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("stream start failed"))

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.Stream(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err == nil || err.Error() != "stream start failed" {
		t.Fatalf("got %v, want stream start failed", err)
	}
}

func TestTemporalRuntime_GetRunHandle_NilClient(t *testing.T) {
	rt := &TemporalRuntime{logger: logger.NoopLogger()}
	_, err := rt.GetRunHandle(context.Background(), "run-1")
	if err == nil || !strings.Contains(err.Error(), "requires a Temporal client") {
		t.Fatalf("got %v, want client required error", err)
	}
}

func TestTemporalRuntime_GetRunHandle_DescribeError(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "run-desc-err"
	workflowID := "agent-run-agent-a-" + runID

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(nil, errors.New("describe unavailable")).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.GetRunHandle(context.Background(), runID)
	if err == nil || err.Error() != "describe unavailable" {
		t.Fatalf("got %v, want describe unavailable", err)
	}
}

func TestTemporalRuntime_GetStreamHandle_NilClient(t *testing.T) {
	rt := &TemporalRuntime{logger: logger.NoopLogger()}
	_, err := rt.GetStreamHandle(context.Background(), "run-1")
	if err == nil || !strings.Contains(err.Error(), "requires a Temporal client") {
		t.Fatalf("got %v, want client required error", err)
	}
}

func TestTemporalRuntime_GetStreamHandle_DescribeError(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "stream-desc-err"
	workflowID := "agent-stream-agent-a-" + runID

	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, "").
		Return(nil, errors.New("describe unavailable")).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rt.GetStreamHandle(context.Background(), runID)
	if err == nil || err.Error() != "describe unavailable" {
		t.Fatalf("got %v, want describe unavailable", err)
	}
}

func TestTemporalRuntime_GetStreamHandle_EventsWithOffset(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	runID := "stream-reconnect"
	workflowID := "agent-stream-agent-a-" + runID

	release := make(chan struct{})
	wfRun := blockingWorkflowRun(release, &types.AgentRunResult{Content: "ok", AgentName: "agent-a"}, nil)

	// GetStreamHandle + Events status gate (running); Subscribe ends via terminal describe.
	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, mock.Anything).
		Return(describeWorkflowRunning(), nil).Twice()
	stubWorkflowStreamSubscribeEnd(tc)
	tc.On("DescribeWorkflowExecution", mock.Anything, workflowID, mock.Anything).
		Return(describeWorkflowCompleted(), nil).Maybe()
	tc.On("GetWorkflow", mock.Anything, workflowID, "").Return(wfRun).Maybe()
	tc.On("CancelWorkflow", mock.Anything, workflowID, "").Return(nil).Maybe()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := rt.GetStreamHandle(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}

	ch, err := h.Events(context.Background(), 42)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	close(release)
	evs := collectStreamEvents(t, ch, 3*time.Second)
	gotTypes := streamEventTypes(evs)

	for _, typ := range gotTypes {
		if typ == events.AgentEventTypeRunStarted {
			t.Fatal("fromOffset > 0 should skip synthetic RUN_STARTED")
		}
	}
	sawFinished := false
	for _, typ := range gotTypes {
		if typ == events.AgentEventTypeRunFinished {
			sawFinished = true
		}
	}
	if !sawFinished {
		t.Fatal("expected RUN_FINISHED after reconnect Events")
	}
	waitHandleDone(t, h.Done())
}

func TestTemporalRuntime_Approve_CompleteActivity(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	token := base64.StdEncoding.EncodeToString([]byte("task-token-bytes"))
	tc.On("CompleteActivity", mock.Anything, []byte("task-token-bytes"), types.ApprovalStatusApproved, mock.Anything).
		Return(nil).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := rt.approve(context.Background(), token, types.ApprovalStatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
}

// TestTemporalRuntime_Approve_SecondCallAlreadyResolved completes the same token twice;
// the second CompleteActivity "not found" must surface as ErrApprovalAlreadyResolved.
func TestTemporalRuntime_Approve_SecondCallAlreadyResolved(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	token := base64.StdEncoding.EncodeToString([]byte("task-token-bytes"))
	tc.On("CompleteActivity", mock.Anything, []byte("task-token-bytes"), types.ApprovalStatusApproved, mock.Anything).
		Return(nil).Once()
	tc.On("CompleteActivity", mock.Anything, []byte("task-token-bytes"), types.ApprovalStatusApproved, mock.Anything).
		Return(errors.New("activity task not found")).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := rt.approve(context.Background(), token, types.ApprovalStatusApproved); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	err = rt.approve(context.Background(), token, types.ApprovalStatusApproved)
	if !errors.Is(err, types.ErrApprovalAlreadyResolved) {
		t.Fatalf("second approve: got %v, want ErrApprovalAlreadyResolved", err)
	}
}

func TestTemporalRuntime_OnApproval_AlreadyResolved(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	token := base64.StdEncoding.EncodeToString([]byte("task-token-bytes"))
	tc.On("CompleteActivity", mock.Anything, []byte("task-token-bytes"), types.ApprovalStatusRejected, mock.Anything).
		Return(errors.New("activity task not found")).Once()

	rt, err := NewTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithDisableLocalWorker(true),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent-a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	err = rt.approve(context.Background(), token, types.ApprovalStatusRejected)
	if !errors.Is(err, types.ErrApprovalAlreadyResolved) {
		t.Fatalf("got %v, want ErrApprovalAlreadyResolved", err)
	}
}

func TestTemporalStatusToRunStatus(t *testing.T) {
	cases := []struct {
		in   enumspb.WorkflowExecutionStatus
		want types.RunStatus
	}{
		{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, types.StatusRunning},
		{enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, types.StatusCompleted},
		{enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, types.StatusFailed},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, types.StatusCancelled},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, types.StatusFailed},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, types.StatusCancelled},
		{enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED, types.StatusPending},
	}
	for _, tc := range cases {
		if got := temporalStatusToRunStatus(tc.in); got != tc.want {
			t.Fatalf("%v: got %q want %q", tc.in, got, tc.want)
		}
	}
}
