package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
)

// expectRuntimeRunCompleted stubs Runtime.Run → a finished RunHandle (closed Done + Get).
func expectRuntimeRunCompleted(
	ctrl *gomock.Controller,
	mockRT *rtmocks.MockRuntime,
	runID string,
	result *types.AgentRunResult,
	assertReq func(*runtime.RunRequest),
) *rtmocks.MockRunHandle {
	h := rtmocks.NewMockRunHandle(ctrl)
	mockRT.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req *runtime.RunRequest) (runtime.RunHandle, error) {
			if assertReq != nil {
				assertReq(req)
			}
			return h, nil
		},
	)
	h.EXPECT().ID().Return(runID).AnyTimes()
	h.EXPECT().Done().Return(closedDoneChan()).AnyTimes()
	if result != nil {
		out := *result
		if out.RunID == "" {
			out.RunID = runID
		}
		h.EXPECT().Get(gomock.Any()).Return(&out, nil).AnyTimes()
	}
	return h
}

func TestAgent_Run_ForwardsRequestAndReturnsResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	const runID = "run-1"
	expectRuntimeRunCompleted(ctrl, mockRT, runID,
		&types.AgentRunResult{Content: "reply", AgentName: "TestAgent", Model: "m1"},
		func(req *runtime.RunRequest) {
			if req.EnableLLMStream {
				t.Error("Run must set EnableLLMStream false")
			}
			if req.UserPrompt != "hello" {
				t.Errorf("UserPrompt = %q", req.UserPrompt)
			}
		},
	)

	a := testAgentWithRuntime(mockRT)
	agentRun, err := a.Run(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := agentRun.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "reply" || resp.Model != "m1" || resp.AgentName != "TestAgent" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.RunID != runID {
		t.Fatalf("RunID = %q, want %q", resp.RunID, runID)
	}
}

func TestAgent_Run_DeliversResultViaHandle(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	const runID = "run-async"
	expectRuntimeRunCompleted(ctrl, mockRT, runID,
		&types.AgentRunResult{Content: "mock", AgentName: "TestAgent", Model: "stub"},
		nil,
	)

	a := testAgentWithRuntime(mockRT)
	agentRun, err := a.Run(context.Background(), "async", nil)
	if err != nil {
		t.Fatal(err)
	}
	if agentRun.ID() != runID {
		t.Fatalf("Run ID = %q, want %q", agentRun.ID(), runID)
	}
	select {
	case <-agentRun.Done():
		result, getErr := agentRun.Get(context.Background())
		if getErr != nil {
			t.Fatal(getErr)
		}
		if result.Content != "mock" {
			t.Fatalf("result.Content = %q, want mock", result.Content)
		}
		if result.RunID != runID {
			t.Fatalf("result.RunID = %q, want %q", result.RunID, runID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for Run result via handle")
	}
}

func TestConversationIDFromOpts(t *testing.T) {
	if got := conversationIDFromOpts(nil); got != "" {
		t.Errorf("nil opts: got %q", got)
	}
	if got := conversationIDFromOpts(&AgentRunOptions{}); got != "" {
		t.Errorf("nil ConversationOptions: got %q", got)
	}
	opts := &AgentRunOptions{ConversationOptions: &ConversationOptions{ID: "session-1"}}
	if got := conversationIDFromOpts(opts); got != "session-1" {
		t.Errorf("got %q, want session-1", got)
	}
}

func TestAgent_Run_ForwardsConversationID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)

	opts := &AgentRunOptions{ConversationOptions: &ConversationOptions{ID: "conv-1"}}
	expectRuntimeRunCompleted(ctrl, mockRT, "run-conv",
		&types.AgentRunResult{Content: "ok"},
		func(req *runtime.RunRequest) {
			if req.ConversationID != "conv-1" {
				t.Errorf("ConversationID = %q, want conv-1", req.ConversationID)
			}
		},
	)

	a := testAgentWithRuntime(mockRT)
	a.conversationConfig = &conversation.Config{Conversation: ifacemocks.NewMockConversation(ctrl)}
	agentRun, err := a.Run(context.Background(), "hello", opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentRun.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAgent_Run_RequiresApprovalHandlerWhenToolsNeedApproval(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)

	a := &Agent{
		agentConfig: agentConfig{
			Name:    "A",
			logger:  logger.DefaultLogger("error"),
			tracer:  observability.DefaultNoopTracer,
			metrics: observability.DefaultNoopMetrics,
			tools: []interfaces.Tool{
				testToolWithApproval(t, "need", true),
			},
		},
		runtime: mockRT,
	}
	if err := a.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	_, err := a.Run(context.Background(), "hi", nil)
	if err == nil || !strings.Contains(err.Error(), "WithApprovalHandler") {
		t.Fatalf("got %v", err)
	}
}

func TestAgent_Run_resolvesToolsPerRun(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)

	reg := NewToolRegistry()
	if err := reg.Register(testTool(t, "first")); err != nil {
		t.Fatal(err)
	}

	cfg := agentConfig{
		Name:               "TestAgent",
		toolRegistry:       reg,
		logger:             logger.DefaultLogger("error"),
		maxSubAgentDepth:   2,
		tracer:             observability.DefaultNoopTracer,
		metrics:            observability.DefaultNoopMetrics,
		toolApprovalPolicy: AutoToolApprovalPolicy(),
	}
	mustTestRegistries(t, &cfg)
	a := &Agent{
		agentConfig: cfg,
		runtime:     mockRT,
		runs:        store.NewKV[string, *agentRun](),
		streams:     store.NewKV[string, *agentStream](),
	}

	var toolCounts []int
	expectRuntimeRunCompleted(ctrl, mockRT, "run-tools-1",
		&types.AgentRunResult{Content: "ok"},
		func(req *runtime.RunRequest) { toolCounts = append(toolCounts, len(req.Tools)) },
	)
	expectRuntimeRunCompleted(ctrl, mockRT, "run-tools-2",
		&types.AgentRunResult{Content: "ok"},
		func(req *runtime.RunRequest) { toolCounts = append(toolCounts, len(req.Tools)) },
	)

	agentRun1, err := a.Run(context.Background(), "one", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentRun1.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.ToolRegistry().Register(testTool(t, "second")); err != nil {
		t.Fatal(err)
	}
	agentRun2, err := a.Run(context.Background(), "two", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentRun2.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(toolCounts) != 2 || toolCounts[0] != 1 || toolCounts[1] != 2 {
		t.Fatalf("tool counts per run = %v, want [1 2]", toolCounts)
	}
}

func TestAgent_GetAgentRun_TerminalReturnsAlreadyCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().GetRunHandle(gomock.Any(), "run-done").Return(nil, types.ErrRunAlreadyCompleted)

	a := testAgentWithRuntime(mockRT)
	agentRun, err := a.GetAgentRun(context.Background(), "run-done")
	if !errors.Is(err, types.ErrRunAlreadyCompleted) {
		t.Fatalf("err = %v, want ErrRunAlreadyCompleted", err)
	}
	if agentRun != nil {
		t.Fatal("expected nil handle when run already completed")
	}
}

func TestAgent_GetAgentRun_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	mockRT.EXPECT().GetRunHandle(gomock.Any(), "missing").Return(nil, types.ErrRunNotFound)

	a := testAgentWithRuntime(mockRT)
	_, err := a.GetAgentRun(context.Background(), "missing")
	if !errors.Is(err, types.ErrRunNotFound) {
		t.Fatalf("err = %v, want ErrRunNotFound", err)
	}
}

func TestAgent_GetAgentRun_ReturnsSameHandleInProcess(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	const runID = "run-shared"
	h := rtmocks.NewMockRunHandle(ctrl)
	done := make(chan struct{})
	release := make(chan struct{})
	mockRT.EXPECT().Run(gomock.Any(), gomock.Any()).Return(h, nil)
	h.EXPECT().ID().Return(runID).AnyTimes()
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()
	h.EXPECT().Get(gomock.Any()).DoAndReturn(func(context.Context) (*types.AgentRunResult, error) {
		<-release
		return &types.AgentRunResult{Content: "ok", RunID: runID}, nil
	})
	// GetAgentRun must not call GetRunHandle when the Run handle is still registered.

	a := testAgentWithRuntime(mockRT)
	h1, err := a.Run(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}

	h2, err := a.GetAgentRun(context.Background(), h1.ID())
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("GetAgentRun must return the same in-process handle as Run")
	}
	h3, err := a.GetAgentRun(context.Background(), h1.ID())
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h3 {
		t.Fatal("repeated GetAgentRun must return the same handle")
	}

	close(release)
	res, err := h2.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.Content != "ok" {
		t.Fatalf("Get via shared handle = %+v", res)
	}
	close(done)
}

func TestAgent_GetAgentRun_UnregistersWhenDone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	expectRuntimeRunCompleted(ctrl, mockRT, "run-done-unreg",
		&types.AgentRunResult{Content: "done"}, nil)
	// After awaitCompletion removes the map entry, GetAgentRun hits the runtime.
	mockRT.EXPECT().GetRunHandle(gomock.Any(), "run-done-unreg").Return(nil, types.ErrRunAlreadyCompleted)

	a := testAgentWithRuntime(mockRT)
	h1, err := a.Run(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-h1.Done()

	// Wait for awaitCompletion to DeleteIf this handle out of a.runs.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := a.runs.Get(h1.ID()); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for run registry unregister after Done")
		}
		time.Sleep(5 * time.Millisecond)
	}

	h2, err := a.GetAgentRun(context.Background(), h1.ID())
	if !errors.Is(err, ErrRunAlreadyCompleted) {
		t.Fatalf("err = %v, want ErrRunAlreadyCompleted", err)
	}
	if h2 != nil {
		t.Fatal("expected nil handle after run completed")
	}
}

func TestNewAgentRun_NilRegistry_NoTracking(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	h := rtmocks.NewMockRunHandle(ctrl)
	h.EXPECT().ID().Return("run-nil-reg").AnyTimes()
	// Done must not be required: nil registry skips awaitCompletion goroutine.

	r := newAgentRun(h, nil)
	if r == nil || r.ID() != "run-nil-reg" {
		t.Fatalf("newAgentRun(nil registry) = %+v", r)
	}
}

func TestNewAgentRun_RegistersAndUnregistersOnDone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const runID = "run-reg"
	h := rtmocks.NewMockRunHandle(ctrl)
	done := make(chan struct{})
	h.EXPECT().ID().Return(runID).AnyTimes()
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()

	runs := store.NewKV[string, *agentRun]()
	r := newAgentRun(h, runs)
	got, ok := runs.Get(runID)
	if !ok || got != r {
		t.Fatal("newAgentRun must register handle in runs map")
	}

	close(done)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := runs.Get(runID); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for DeleteIf after Done")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewAgentRun_DeleteIfKeepsReplacement(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const runID = "run-replace"

	h1 := rtmocks.NewMockRunHandle(ctrl)
	done1 := make(chan struct{})
	h1.EXPECT().ID().Return(runID).AnyTimes()
	h1.EXPECT().Done().Return((<-chan struct{})(done1)).AnyTimes()

	h2 := rtmocks.NewMockRunHandle(ctrl)
	done2 := make(chan struct{})
	h2.EXPECT().ID().Return(runID).AnyTimes()
	h2.EXPECT().Done().Return((<-chan struct{})(done2)).AnyTimes()

	runs := store.NewKV[string, *agentRun]()
	_ = newAgentRun(h1, runs)
	r2 := newAgentRun(h2, runs) // replaces first handle under the same runID
	if got, _ := runs.Get(runID); got != r2 {
		t.Fatal("map should hold the replacement handle")
	}

	close(done1) // first waiter's DeleteIf must not remove r2
	time.Sleep(50 * time.Millisecond)
	if got, ok := runs.Get(runID); !ok || got != r2 {
		t.Fatal("finished predecessor must not remove replacement via DeleteIf")
	}

	close(done2)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := runs.Get(runID); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for replacement unregister")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAgent_GetAgentRun_ReconnectLiveHandle(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	h := rtmocks.NewMockRunHandle(ctrl)
	done := make(chan struct{})
	close(done)
	mockRT.EXPECT().GetRunHandle(gomock.Any(), "live-run").Return(h, nil)
	h.EXPECT().ID().Return("live-run").AnyTimes()
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()

	a := testAgentWithRuntime(mockRT)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agentRun, err := a.GetAgentRun(ctx, "live-run")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-agentRun.Done():
	case <-ctx.Done():
		t.Fatal("timeout waiting for reconnect handle Done")
	}
}

func TestAgent_Run_CancelAndStatusWhileLive(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	const runID = "run-live"
	h := rtmocks.NewMockRunHandle(ctrl)
	done := make(chan struct{})
	mockRT.EXPECT().Run(gomock.Any(), gomock.Any()).Return(h, nil)
	h.EXPECT().ID().Return(runID).AnyTimes()
	h.EXPECT().Done().Return((<-chan struct{})(done)).AnyTimes()
	h.EXPECT().Status(gomock.Any()).Return(types.StatusRunning, nil)
	h.EXPECT().Cancel(gomock.Any()).DoAndReturn(func(context.Context) error {
		close(done)
		return nil
	})

	a := testAgentWithRuntime(mockRT)
	agentRun, err := a.Run(context.Background(), "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := agentRun.Status(context.Background())
	if err != nil || status != StatusRunning {
		t.Fatalf("Status = %v, %v", status, err)
	}
	if err := agentRun.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-agentRun.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for Done")
	}
}
