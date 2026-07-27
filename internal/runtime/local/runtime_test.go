package local

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	ifmocks "github.com/agenticenv/agent-sdk-go/pkg/interfaces/mocks"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Shared test stubs
// ---------------------------------------------------------------------------

// seqLLMClient returns LLM responses from a pre-loaded sequence.
// Once the sequence is exhausted it returns a plain "done" response.
type seqLLMClient struct {
	mu        sync.Mutex
	responses []*interfaces.LLMResponse
	errs      []error
	call      int
}

func (s *seqLLMClient) Generate(_ context.Context, _ *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.call
	s.call++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i < len(s.responses) {
		return s.responses[i], nil
	}
	return &interfaces.LLMResponse{Content: "done"}, nil
}
func (s *seqLLMClient) GenerateStream(_ context.Context, _ *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stream not implemented in seqLLMClient")
}
func (s *seqLLMClient) GetModel() string                    { return "test-model" }
func (s *seqLLMClient) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (s *seqLLMClient) IsStreamSupported() bool             { return false }

// stubTool is a minimal Tool with configurable execute result and optional approval.
type stubTool struct {
	name          string
	result        string
	execErr       error
	needsApproval bool
}

func (t stubTool) Name() string                      { return t.name }
func (t stubTool) DisplayName() string               { return t.name }
func (t stubTool) Description() string               { return "" }
func (t stubTool) Parameters() interfaces.JSONSchema { return nil }
func (t stubTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	return t.result, t.execErr
}
func (t stubTool) ApprovalRequired() bool { return t.needsApproval }

// newLocalRT constructs a LocalRuntime suitable for tests.
func newLocalRT(t *testing.T, client interfaces.LLMClient, tools ...interfaces.Tool) *LocalRuntime {
	t.Helper()
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test-agent", SystemPrompt: "you are helpful"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: client},
			Limits: sdkruntime.AgentLimits{
				MaxIterations: 5,
				Timeout:       30 * time.Second,
			},
		}),
	)
	require.NoError(t, err)
	_ = tools // callers pass resolved tools on RunRequest.Tools
	return rt
}

func runReq(prompt string, tools ...interfaces.Tool) *sdkruntime.RunRequest {
	return &sdkruntime.RunRequest{UserPrompt: prompt, Tools: tools}
}

// collectEvents drains an event channel until it is closed or timeout elapses,
// returning all events received.
func collectEvents(t *testing.T, ch <-chan events.AgentEvent, timeout time.Duration) []events.AgentEvent {
	t.Helper()
	var collected []events.AgentEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return collected
			}
			if ev != nil {
				collected = append(collected, ev)
			}
		case <-deadline:
			t.Fatalf("collectEvents: timed out after %s waiting for channel to close", timeout)
			return collected
		}
	}
}

// eventTypes extracts the AgentEventType from each collected event.
func eventTypes(evs []events.AgentEvent) []events.AgentEventType {
	out := make([]events.AgentEventType, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type()
	}
	return out
}

// waitHandleStatus polls handle.Status until want or timeout.
func waitHandleStatus(t *testing.T, h interface {
	Status(context.Context) (types.RunStatus, error)
}, want types.RunStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		st, err := h.Status(context.Background())
		if err == nil && st == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for status %s (last=%v err=%v)", want, st, err)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// NewLocalRuntime
// ---------------------------------------------------------------------------

func TestNewLocalRuntime_MissingLLMClient(t *testing.T) {
	_, err := NewLocalRuntime(
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{}),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "llm client is required")
}

func TestNewLocalRuntime_DefaultNoopObservability(t *testing.T) {
	rt, err := NewLocalRuntime(
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: &seqLLMClient{}},
		}),
	)
	require.NoError(t, err)
	require.Equal(t, observability.DefaultNoopTracer, rt.Tracer)
	require.Equal(t, observability.DefaultNoopMetrics, rt.Metrics)
}

func TestNewLocalRuntime_WithAllOptions(t *testing.T) {
	ctrl := gomock.NewController(t)
	tracer := ifmocks.NewMockTracer(ctrl)
	metrics := ifmocks.NewMockMetrics(ctrl)

	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "my-agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: &seqLLMClient{}},
		}),
		WithTracer(tracer),
		WithMetrics(metrics),
		WithToolExecutionMode(types.AgentToolExecutionModeSequential),
	)
	require.NoError(t, err)
	require.Equal(t, "my-agent", rt.AgentSpec.Name)
	require.Equal(t, tracer, rt.Tracer)
	require.Equal(t, metrics, rt.Metrics)
	require.Equal(t, types.AgentToolExecutionModeSequential, rt.ToolExecutionMode)
}

func TestNewLocalRuntime_EventBusInitialised(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	require.NotNil(t, rt.eventbus, "eventbus should be initialised by NewLocalRuntime")
	require.True(t, rt.ownsEventBus, "NewLocalRuntime should own the bus it creates")
}

// ---------------------------------------------------------------------------
// Run / GetRunHandle
// ---------------------------------------------------------------------------

func TestRun_NilRequest(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	_, err := rt.Run(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil RunRequest")
}

func TestRun_SimpleTextResponse(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{Content: "Hello from the agent"},
		},
	}
	rt := newLocalRT(t, client)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)
	require.NotEmpty(t, handle.ID())

	result, err := handle.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Hello from the agent", result.Content)
	require.Equal(t, "test-agent", result.AgentName)
	require.Equal(t, "test-model", result.Model)
	require.Equal(t, handle.ID(), result.RunID)

	st, err := handle.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCompleted, st)
}

func TestGetRunHandle_NotFound(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	_, err := rt.GetRunHandle(context.Background(), "unknown-run-id")
	require.ErrorIs(t, err, types.ErrRunNotFound)
}

func TestRun_CancelAbortsLiveRun(t *testing.T) {
	blocking := &blockingLLMClient{block: make(chan struct{})}
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test-agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: blocking},
			Limits: sdkruntime.AgentLimits{
				MaxIterations: 1,
				Timeout:       30 * time.Second,
			},
		}),
	)
	require.NoError(t, err)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	waitHandleStatus(t, handle, types.StatusRunning, 2*time.Second)
	require.NoError(t, handle.Cancel(context.Background()))

	_, getErr := handle.Get(context.Background())
	require.Error(t, getErr)
	require.ErrorIs(t, getErr, context.Canceled)

	st, err := handle.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, types.StatusCancelled, st)
}

func TestRun_PropagatesLLMError(t *testing.T) {
	client := &seqLLMClient{
		errs: []error{errors.New("llm unavailable")},
	}
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test-agent", SystemPrompt: "you are helpful"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: client},
			Limits: sdkruntime.AgentLimits{
				MaxIterations: 5,
				Timeout:       30 * time.Second,
			},
			ExecutionConfigs: sdkruntime.ExecutionConfigs{
				LLM: sdkruntime.ExecutionConfig{MaxAttempts: 1},
			},
		}),
	)
	require.NoError(t, err)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	_, err = handle.Get(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "llm unavailable")
}

func TestRun_AppliesTimeoutWhenNoDeadline(t *testing.T) {
	blocking := &blockingLLMClient{block: make(chan struct{})}
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: blocking},
			Limits: sdkruntime.AgentLimits{
				MaxIterations: 1,
				Timeout:       50 * time.Millisecond,
			},
		}),
	)
	require.NoError(t, err)

	start := time.Now()
	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	_, err = handle.Get(context.Background())
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 2*time.Second, "runtime timeout should fire well before 2s")
}

// blockingLLMClient blocks until its context is cancelled.
type blockingLLMClient struct {
	block chan struct{}
}

func (b *blockingLLMClient) Generate(ctx context.Context, _ *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (b *blockingLLMClient) GenerateStream(_ context.Context, _ *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("not supported")
}
func (b *blockingLLMClient) GetModel() string                    { return "blocking" }
func (b *blockingLLMClient) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (b *blockingLLMClient) IsStreamSupported() bool             { return false }

func TestRun_WithApprovalHandler(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{
				ToolCalls: []*interfaces.ToolCall{
					{ToolCallID: "c1", ToolName: "approve-tool"},
				},
			},
			{Content: "tool done"},
		},
	}
	tool := stubTool{name: "approve-tool", result: "executed", needsApproval: true}

	handlerCalled := false
	handler := func(_ context.Context, req *types.ApprovalRequest) {
		handlerCalled = true
		_ = req.Respond(types.ApprovalStatusApproved)
	}

	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test-agent", SystemPrompt: "you are helpful"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: client},
			Limits: sdkruntime.AgentLimits{
				MaxIterations: 5,
				Timeout:       30 * time.Second,
			},
		}),
		WithApprovalHandler(handler),
	)
	require.NoError(t, err)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{
		UserPrompt: "run tool",
		Tools:      []interfaces.Tool{tool},
	})
	require.NoError(t, err)

	result, err := handle.Get(context.Background())
	require.NoError(t, err)
	require.True(t, handlerCalled, "approval handler must be called")
	require.Equal(t, "tool done", result.Content)
}

func TestRun_ToolCallThenFinalAnswer(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{
				ToolCalls: []*interfaces.ToolCall{
					{ToolCallID: "c1", ToolName: "calc"},
				},
			},
			{Content: "the answer is 42"},
		},
	}
	tool := stubTool{name: "calc", result: "42"}
	rt := newLocalRT(t, client, tool)

	handle, err := rt.Run(context.Background(), runReq("compute", tool))
	require.NoError(t, err)

	result, err := handle.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "the answer is 42", result.Content)
}

func TestRun_PersistsConversationMessages(t *testing.T) {
	ctrl := gomock.NewController(t)
	conv := ifmocks.NewMockConversation(ctrl)

	conv.EXPECT().ListMessages(gomock.Any(), "conv-1", gomock.Any()).Return(nil, nil)
	conv.EXPECT().AddMessage(gomock.Any(), "conv-1", gomock.Any()).Return(nil).Times(2)

	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{{Content: "persisted"}},
	}
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM:     sdkruntime.AgentLLM{Client: client},
			Session: sdkruntime.AgentSession{Conversation: conv, ConversationSize: 20},
			Limits:  sdkruntime.AgentLimits{MaxIterations: 5, Timeout: 5 * time.Second},
		}),
	)
	require.NoError(t, err)

	handle, err := rt.Run(context.Background(), &sdkruntime.RunRequest{
		UserPrompt:     "remember this",
		ConversationID: "conv-1",
	})
	require.NoError(t, err)

	_, err = handle.Get(context.Background())
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Stream / GetStreamHandle
// ---------------------------------------------------------------------------

func TestStream_NilRequest(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	_, err := rt.Stream(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil RunRequest")
}

func TestGetStreamHandle_NotFound(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	_, err := rt.GetStreamHandle(context.Background(), "unknown-run-id")
	require.ErrorIs(t, err, types.ErrStreamNotFound)
}

func TestStream_EmitsRunStartedAndFinished(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{{Content: "stream answer"}},
	}
	rt := newLocalRT(t, client)

	ctx := context.Background()
	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hello"})
	require.NoError(t, err)

	ch, err := handle.Events(ctx, 0)
	require.NoError(t, err)

	evs := collectEvents(t, ch, 5*time.Second)
	gotTypes := eventTypes(evs)

	require.Contains(t, gotTypes, events.AgentEventTypeRunStarted)
	require.Contains(t, gotTypes, events.AgentEventTypeRunFinished)
	require.Equal(t, events.AgentEventTypeRunStarted, gotTypes[0])
	require.Equal(t, events.AgentEventTypeRunFinished, gotTypes[len(gotTypes)-1])

	st, err := handle.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, types.StatusCompleted, st)
}

func TestStream_EmitsRunError(t *testing.T) {
	client := &seqLLMClient{
		errs: []error{errors.New("llm down")},
	}
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test-agent", SystemPrompt: "you are helpful"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: client},
			Limits: sdkruntime.AgentLimits{
				MaxIterations: 5,
				Timeout:       30 * time.Second,
			},
			ExecutionConfigs: sdkruntime.ExecutionConfigs{
				LLM: sdkruntime.ExecutionConfig{MaxAttempts: 1},
			},
		}),
	)
	require.NoError(t, err)

	ctx := context.Background()
	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	ch, err := handle.Events(ctx, 0)
	require.NoError(t, err)

	evs := collectEvents(t, ch, 5*time.Second)
	gotTypes := eventTypes(evs)

	require.Contains(t, gotTypes, events.AgentEventTypeRunStarted)
	require.Contains(t, gotTypes, events.AgentEventTypeRunError)

	st, err := handle.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, types.StatusFailed, st)
}

func TestStream_ChannelClosedAfterTerminalEvent(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{{Content: "done"}},
	}
	rt := newLocalRT(t, client)

	ctx := context.Background()
	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	ch, err := handle.Events(ctx, 0)
	require.NoError(t, err)

	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("channel never closed")
		}
	}
}

func TestStream_Events_AlreadyCompleted(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{{Content: "done"}},
	}
	rt := newLocalRT(t, client)

	ctx := context.Background()
	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	ch, err := handle.Events(ctx, 0)
	require.NoError(t, err)
	_ = collectEvents(t, ch, 5*time.Second)

	waitHandleStatus(t, handle, types.StatusCompleted, 2*time.Second)

	_, err = handle.Events(ctx, 0)
	require.ErrorIs(t, err, types.ErrRunAlreadyCompleted)
}

func TestStream_Events_OffsetUnsupported(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{{Content: "done"}},
	}
	rt := newLocalRT(t, client)

	handle, err := rt.Stream(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	_, err = handle.Events(context.Background(), 5)
	require.ErrorIs(t, err, types.ErrStreamOffsetNotSupported)

	// Still allow offset-0 subscribe so the stream goroutine can finish.
	ch, err := handle.Events(context.Background(), 0)
	require.NoError(t, err)
	_ = collectEvents(t, ch, 5*time.Second)
}

func TestStream_Status_RunningThenTerminal(t *testing.T) {
	blocking := &blockingLLMClient{block: make(chan struct{})}
	rt := newLocalRT(t, blocking)

	ctx := context.Background()
	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	st, err := handle.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, types.StatusRunning, st)

	ch, err := handle.Events(ctx, 0)
	require.NoError(t, err)

	require.NoError(t, handle.Cancel(ctx))
	_ = collectEvents(t, ch, 3*time.Second)

	st, err = handle.Status(context.Background())
	require.NoError(t, err)
	require.True(t, st.IsTerminal(), "expected terminal status, got %s", st)
	require.Equal(t, types.StatusCancelled, st)
}

func TestStream_Cancel_AbortsLiveStream(t *testing.T) {
	blocking := &blockingLLMClient{block: make(chan struct{})}
	rt, err := NewLocalRuntime(
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test-agent"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: blocking},
			Limits: sdkruntime.AgentLimits{
				MaxIterations: 1,
				Timeout:       30 * time.Second,
			},
		}),
	)
	require.NoError(t, err)

	ctx := context.Background()
	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	ch, err := handle.Events(ctx, 0)
	require.NoError(t, err)

	require.NoError(t, handle.Cancel(ctx))

	evs := collectEvents(t, ch, 3*time.Second)
	require.NotEmpty(t, evs)
	require.Contains(t, eventTypes(evs), events.AgentEventTypeRunStarted)
}

func TestStream_ContextCancelledAborts(t *testing.T) {
	blocking := &blockingLLMClient{block: make(chan struct{})}
	rt := newLocalRT(t, blocking)

	ctx, cancel := context.WithCancel(context.Background())

	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	require.NoError(t, err)

	ch, err := handle.Events(context.Background(), 0)
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)
	cancel()

	evs := collectEvents(t, ch, 3*time.Second)
	require.Contains(t, eventTypes(evs), events.AgentEventTypeRunStarted)
}

// ---------------------------------------------------------------------------
// Approve
// ---------------------------------------------------------------------------

func TestApprove_UnknownToken(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	err := rt.approve(context.Background(), "nonexistent-token", types.ApprovalStatusApproved)
	require.ErrorIs(t, err, types.ErrApprovalAlreadyResolved)
}

func TestApprove_ResolvesRegisteredChannel(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})

	const token = "test-token-123"
	resultCh := make(chan types.ApprovalStatus, 1)
	rt.pendingApprovals.Store(token, resultCh)

	err := rt.approve(context.Background(), token, types.ApprovalStatusApproved)
	require.NoError(t, err)

	select {
	case status := <-resultCh:
		require.Equal(t, types.ApprovalStatusApproved, status)
	case <-time.After(time.Second):
		t.Fatal("expected status on channel, got timeout")
	}

	_, loaded := rt.pendingApprovals.Load(token)
	require.False(t, loaded, "token must be removed after approve")
}

func TestApprove_RejectsViaSameToken(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})

	const token = "reject-token"
	resultCh := make(chan types.ApprovalStatus, 1)
	rt.pendingApprovals.Store(token, resultCh)

	err := rt.approve(context.Background(), token, types.ApprovalStatusRejected)
	require.NoError(t, err)

	status := <-resultCh
	require.Equal(t, types.ApprovalStatusRejected, status)
}

func TestApprove_DoubleApproveSecondErrors(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})

	const token = "double-token"
	resultCh := make(chan types.ApprovalStatus, 1)
	rt.pendingApprovals.Store(token, resultCh)

	require.NoError(t, rt.approve(context.Background(), token, types.ApprovalStatusApproved))
	err := rt.approve(context.Background(), token, types.ApprovalStatusApproved)
	require.ErrorIs(t, err, types.ErrApprovalAlreadyResolved)
}

func TestApprove_StreamingEndToEnd(t *testing.T) {
	client := &seqLLMClient{
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{
				{ToolCallID: "c1", ToolName: "guarded-tool"},
			}},
			{Content: "approved result"},
		},
	}
	tool := stubTool{name: "guarded-tool", result: "ran!", needsApproval: true}
	rt := newLocalRT(t, client, tool)

	ctx := context.Background()
	handle, err := rt.Stream(ctx, &sdkruntime.RunRequest{
		UserPrompt: "run guarded tool",
		Tools:      []interfaces.Tool{tool},
	})
	require.NoError(t, err)

	ch, err := handle.Events(ctx, 0)
	require.NoError(t, err)

	var approvalToken string
	var allEvents []events.AgentEvent

	timeout := time.After(5 * time.Second)
outer:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break outer
			}
			if ev == nil {
				continue
			}
			allEvents = append(allEvents, ev)
			if ev.Type() == events.AgentEventTypeCustom {
				val, parseErr := events.ParseCustomEventApproval(ev.(*events.AgentCustomEvent))
				if parseErr == nil && val.ApprovalToken != "" {
					approvalToken = val.ApprovalToken
					go func(tok string) {
						_ = rt.approve(context.Background(), tok, types.ApprovalStatusApproved)
					}(approvalToken)
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for streaming events")
		}
	}

	gotTypes := eventTypes(allEvents)
	require.NotEmpty(t, approvalToken, "expected an approval token in CUSTOM event")
	require.Contains(t, gotTypes, events.AgentEventTypeRunFinished)
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestClose_NoError(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	require.NotPanics(t, rt.Close)
	require.False(t, rt.ownsEventBus, "Close should clear ownership after closing owned bus")
	require.NotPanics(t, rt.Close) // idempotent
}

func TestClose_DoesNotCloseSharedEventBus(t *testing.T) {
	parent := newLocalRT(t, &seqLLMClient{})
	child := newLocalRT(t, &seqLLMClient{})
	shared := parent.eventbus

	child.setEventBus(shared)
	require.False(t, child.ownsEventBus)

	child.Close()

	// Parent still owns the bus; publish must still work.
	require.NoError(t, shared.Publish(context.Background(), "ch", []byte("ok")))
	require.True(t, parent.ownsEventBus)
}

// ---------------------------------------------------------------------------
// publishLifecycleEvent
// ---------------------------------------------------------------------------

func TestPublishLifecycleEvent_NilEventbus(t *testing.T) {
	rt := &LocalRuntime{
		Runtime: base.Runtime{
			AgentSpec: sdkruntime.AgentSpec{Name: "a"},
		},
		logger: logger.NoopLogger(),
	}
	require.NotPanics(t, func() {
		rt.publishLifecycleEvent("some-channel", events.NewAgentRunErrorEvent("oops"))
	})
}

func TestPublishLifecycleEvent_EmptyChannel(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	require.NotPanics(t, func() {
		rt.publishLifecycleEvent("", events.NewAgentRunErrorEvent("oops"))
	})
}

func TestPublishLifecycleEvent_NilEvent(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	require.NotPanics(t, func() {
		rt.publishLifecycleEvent("ch", nil)
	})
}

// ---------------------------------------------------------------------------
// event bus sharing
// ---------------------------------------------------------------------------

func TestSetEventBus_ReplacesBus(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	original := rt.eventbus
	require.True(t, rt.ownsEventBus)

	rt2 := newLocalRT(t, &seqLLMClient{})
	newBus := rt2.eventbus

	rt.setEventBus(newBus)
	require.Same(t, newBus, rt.eventbus)
	require.NotSame(t, original, rt.eventbus)
	require.False(t, rt.ownsEventBus, "setEventBus must clear ownership of a shared bus")
}

func TestShareEventBusWithSubAgents(t *testing.T) {
	parent := newLocalRT(t, &seqLLMClient{})
	child := newLocalRT(t, &seqLLMClient{})
	grandchild := newLocalRT(t, &seqLLMClient{})

	parent.shareEventBusWithSubAgents([]*sdkruntime.SubAgentSpec{
		{
			Name:     "child",
			ToolName: "child",
			Runtime:  child,
			Children: []*sdkruntime.SubAgentSpec{
				{Name: "gc", ToolName: "gc", Runtime: grandchild},
			},
		},
	})

	require.Same(t, parent.eventbus, child.eventbus)
	require.Same(t, parent.eventbus, grandchild.eventbus)
	require.False(t, child.ownsEventBus)
	require.False(t, grandchild.ownsEventBus)
	require.True(t, parent.ownsEventBus)
}

// ---------------------------------------------------------------------------
// localChannelName
// ---------------------------------------------------------------------------

func TestLocalChannelName(t *testing.T) {
	name := localChannelName("run-42")
	require.Equal(t, "agent-event-run-42", name)
}

// ---------------------------------------------------------------------------
// subscribeToAgentEvents
// ---------------------------------------------------------------------------

func TestSubscribeToAgentEvents_DecodesEvents(t *testing.T) {
	rt := newLocalRT(t, &seqLLMClient{})
	ctx := context.Background()
	ch, closeFn, err := rt.subscribeToAgentEvents(ctx, "test-channel")
	require.NoError(t, err)
	defer func() { _ = closeFn() }()

	ev := events.NewAgentRunStartedEvent("thread-1", "run-1")
	rt.publishLifecycleEvent("test-channel", ev)

	select {
	case received := <-ch:
		require.Equal(t, events.AgentEventTypeRunStarted, received.Type())
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}
