package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/golang/mock/gomock"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	rtmocks "github.com/agenticenv/agent-sdk-go/internal/runtime/mocks"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
)

// expectRuntimeRun stubs Runtime.Run → RunHandle.Get for non-streaming A2A paths.
func expectRuntimeRun(ctrl *gomock.Controller, mockRT *rtmocks.MockRuntime, result *types.AgentRunResult, runErr error) {
	if runErr != nil {
		mockRT.EXPECT().Run(gomock.Any(), gomock.Any()).Return(nil, runErr)
		return
	}
	h := rtmocks.NewMockRunHandle(ctrl)
	mockRT.EXPECT().Run(gomock.Any(), gomock.Any()).Return(h, nil)
	h.EXPECT().ID().Return("a2a-run").AnyTimes()
	h.EXPECT().Done().Return(closedDoneChan()).AnyTimes()
	h.EXPECT().Get(gomock.Any()).Return(result, nil)
}

// expectRuntimeStream stubs Runtime.Stream → StreamHandle.Events for streaming A2A paths.
// Done is closed so agentStream.awaitCompletion can finish before the test/controller teardown.
func expectRuntimeStream(ctrl *gomock.Controller, mockRT *rtmocks.MockRuntime, ch <-chan events.AgentEvent, streamErr error) {
	if streamErr != nil {
		mockRT.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(nil, streamErr)
		return
	}
	h := rtmocks.NewMockStreamHandle(ctrl)
	mockRT.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(h, nil)
	h.EXPECT().ID().Return("stream-run").AnyTimes()
	h.EXPECT().Done().Return(closedDoneChan()).AnyTimes()
	h.EXPECT().Events(gomock.Any(), int64(0)).Return(ch, nil)
}

func TestRunA2A_NotConfigured(t *testing.T) {
	a := testAgentWithRuntime(nil)
	a.runtime = nil // not used by RunA2A error path
	a.a2aServerConfig = nil
	err := a.RunA2A(context.Background())
	if err == nil {
		t.Fatal("expected error when a2a server not configured")
	}
}

func TestRunA2A_AddressAlreadyInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	tcpAddr := ln.Addr().(*net.TCPAddr)

	a := &Agent{
		agentConfig: agentConfig{
			Name:             "dup",
			logger:           logger.DefaultLogger("error"),
			a2aServerConfig:  &A2AServerConfig{Hostname: tcpAddr.IP.String(), Port: tcpAddr.Port},
			maxSubAgentDepth: 2,
		},
	}
	err = a.RunA2A(context.Background())
	if err == nil {
		t.Fatal("expected error when listen address is already bound")
	}
}

func TestBuildSDKAgentCard(t *testing.T) {
	t.Parallel()

	a := &Agent{
		agentConfig: agentConfig{
			Name:            "CardAgent",
			Description:     "desc",
			LLMClient:       testLLM(t),
			a2aServerConfig: &A2AServerConfig{Hostname: "127.0.0.1", Port: 9000},
		},
	}
	mustTestRegistries(t, &a.agentConfig)
	card := a.buildSDKAgentCard()
	if card.Name != "CardAgent" || card.Description != "desc" || card.Version != a2aServerVersion {
		t.Fatalf("card metadata: %+v", card)
	}
	if card.Capabilities.Streaming {
		t.Fatal("Streaming should be false when LLM does not support streaming")
	}
	if len(card.SecuritySchemes) != 0 {
		t.Fatalf("expected no security without bearer tokens, got %v", card.SecuritySchemes)
	}
	if card.SupportedInterfaces[0].URL != "http://127.0.0.1:9000" {
		t.Fatalf("base URL: %v", card.SupportedInterfaces[0].URL)
	}

	a2 := &Agent{
		agentConfig: agentConfig{
			Name:            "S",
			LLMClient:       testStreamLLM(t),
			a2aServerConfig: &A2AServerConfig{Hostname: "localhost", Port: 1},
		},
	}
	mustTestRegistries(t, &a2.agentConfig)
	c2 := a2.buildSDKAgentCard()
	if !c2.Capabilities.Streaming {
		t.Fatal("Streaming should be true when LLM supports streaming")
	}

	a3 := &Agent{
		agentConfig: agentConfig{
			Name:            "AuthCard",
			LLMClient:       testLLM(t),
			a2aServerConfig: &A2AServerConfig{Hostname: "h", Port: 9, BearerTokens: []string{"secret"}},
		},
	}
	mustTestRegistries(t, &a3.agentConfig)
	c3 := a3.buildSDKAgentCard()
	if len(c3.SecuritySchemes) == 0 || len(c3.SecurityRequirements) == 0 {
		t.Fatalf("expected security on card when BearerTokens set: schemes=%v reqs=%v",
			c3.SecuritySchemes, c3.SecurityRequirements)
	}
}

func TestBuildSDKAgentCard_CustomAgentCardOverride(t *testing.T) {
	t.Parallel()

	custom := &interfaces.A2AAgentCard{
		Name:        "PublishedName",
		Description: "Custom discovery text",
		Version:     "9.9.9",
		URL:         "http://127.0.0.1:9000",
		Skills: []interfaces.A2ASkillSpec{
			{ID: "custom-skill", Name: "Custom"},
		},
	}
	a := &Agent{
		agentConfig: agentConfig{
			Name:            "IgnoredName",
			Description:     "ignored desc",
			LLMClient:       testLLM(t),
			a2aServerConfig: &A2AServerConfig{Hostname: "127.0.0.1", Port: 9000, AgentCard: custom},
		},
	}
	got := a.buildSDKAgentCard()
	if got.Name != "PublishedName" || got.Description != "Custom discovery text" || got.Version != "9.9.9" {
		t.Fatalf("expected custom card metadata, got %+v", got)
	}
	if len(got.Skills) != 1 || got.Skills[0].ID != "custom-skill" {
		t.Fatalf("expected custom skills, got %+v", got.Skills)
	}
	if got.SupportedInterfaces[0].URL != "http://127.0.0.1:9000" {
		t.Fatalf("expected interface card URL for transport base, got %q", got.SupportedInterfaces[0].URL)
	}
	if got.Capabilities.Streaming {
		t.Fatal("streaming should follow agent when using interfaces.A2AAgentCard override")
	}
}

func TestDeriveSDKSkills(t *testing.T) {
	t.Parallel()

	a := &Agent{
		agentConfig: agentConfig{
			tools: []interfaces.Tool{
				testToolMeta(t, "alpha", "Alpha", "generic tool", interfaces.JSONSchema{"type": "object"}),
				NewA2ATool("remote", interfaces.ToolSpec{Name: "sk1", Description: "d"},
					interfaces.A2ASkillSpec{
						ID: "sk1", Name: "Skill One", Description: "remote desc",
						Tags: []string{"a2a"}, InputModes: []string{"text/plain"}, OutputModes: []string{"text/plain"},
						Examples: []string{"ex"},
					}, nil),
			},
		},
	}
	if err := a.buildRegistries(); err != nil {
		t.Fatal(err)
	}
	sk := a.deriveSDKSkills()
	if len(sk) != 2 {
		t.Fatalf("want 2 skills, got %d: %+v", len(sk), sk)
	}
	if sk[0].ID != "alpha" || sk[0].Name != "Alpha" {
		t.Fatalf("generic skill: %+v", sk[0])
	}
	if sk[1].ID != "sk1" || sk[1].Name != "Skill One" || len(sk[1].Tags) != 1 || sk[1].Tags[0] != "a2a" {
		t.Fatalf("A2ATool skill should preserve spec: %+v", sk[1])
	}
}

func TestCollectMessageText(t *testing.T) {
	t.Parallel()
	if collectMessageText(nil) != "" {
		t.Fatal("nil message")
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("a"), a2a.NewTextPart("b"))
	if got := collectMessageText(msg); got != "a\nb" {
		t.Fatalf("got %q", got)
	}
	emptyParts := &a2a.Message{Parts: []*a2a.Part{nil}}
	if collectMessageText(emptyParts) != "" {
		t.Fatal("expected empty for nil parts only")
	}
	skipEmpty := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(""), a2a.NewTextPart("only"))
	if collectMessageText(skipEmpty) != "only" {
		t.Fatalf("empty text parts should be skipped, got %q", collectMessageText(skipEmpty))
	}
}

func TestToSDKSkill(t *testing.T) {
	t.Parallel()
	s := interfaces.A2ASkillSpec{
		ID: "id1", Name: "n", Description: "d",
		Tags: []string{"t"}, InputModes: []string{"in"}, OutputModes: []string{"out"}, Examples: []string{"e"},
	}
	got := toSDKSkill(s)
	if got.ID != "id1" || got.Name != "n" || got.Description != "d" ||
		len(got.Tags) != 1 || got.InputModes[0] != "in" || got.OutputModes[0] != "out" || got.Examples[0] != "e" {
		t.Fatalf("%+v", got)
	}
}

func TestAgentA2AExecutor_Execute_NonStreaming(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	expectRuntimeRun(ctrl, mockRT, &types.AgentRunResult{Content: "reply"}, nil)

	a := testAgentWithRuntime(mockRT)
	a.LLMClient = testLLM(t)

	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
		TaskID:  a2a.NewTaskID(),
	}

	var seen []any
	for ev, err := range exec.Execute(context.Background(), execCtx) {
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, ev)
	}
	if len(seen) != 1 {
		t.Fatalf("want 1 event, got %d", len(seen))
	}
	m, ok := seen[0].(*a2a.Message)
	if !ok || m == nil {
		t.Fatalf("want *a2a.Message, got %T", seen[0])
	}
	if collectMessageText(m) != "reply" {
		t.Fatalf("message content: %+v", m)
	}
}

func TestAgentA2AExecutor_Execute_NonStreaming_RunError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	expectRuntimeRun(ctrl, mockRT, nil, errors.New("run boom"))

	a := testAgentWithRuntime(mockRT)
	a.LLMClient = testLLM(t)

	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("x")),
		TaskID:  a2a.NewTaskID(),
	}

	for ev, err := range exec.Execute(context.Background(), execCtx) {
		if err != nil {
			t.Fatal(err)
		}
		su, ok := ev.(*a2a.TaskStatusUpdateEvent)
		if !ok || su == nil {
			t.Fatalf("want status update, got %T", ev)
		}
		if su.Status.State != a2a.TaskStateFailed {
			t.Fatalf("state = %v", su.Status.State)
		}
		return
	}
	t.Fatal("expected status failed event")
}

func TestAgentA2AExecutor_Execute_Streaming_MultiDelta(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	streamCh1 := make(chan events.AgentEvent, 8)
	streamCh1 <- events.NewAgentTextMessageContentEvent("m1", "aa")
	streamCh1 <- events.NewAgentTextMessageContentEvent("m1", "bb")
	streamCh1 <- events.NewAgentRunFinishedEvent("t", "r", &types.AgentRunResult{Content: "full"})
	close(streamCh1)
	var streamRecv1 <-chan events.AgentEvent = streamCh1
	expectRuntimeStream(ctrl, mockRT, streamRecv1, nil)

	a := testAgentWithRuntime(mockRT)
	a.LLMClient = testStreamLLM(t)
	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("prompt")),
		TaskID:  a2a.NewTaskID(),
	}

	var kinds []string
	for ev, err := range exec.Execute(context.Background(), execCtx) {
		if err != nil {
			t.Fatal(err)
		}
		switch x := ev.(type) {
		case *a2a.Task:
			kinds = append(kinds, "Task(submitted)")
		case *a2a.TaskStatusUpdateEvent:
			kinds = append(kinds, fmt.Sprintf("Status:%s", x.Status.State))
		case *a2a.TaskArtifactUpdateEvent:
			kinds = append(kinds, "ArtifactUpdate")
		default:
			kinds = append(kinds, fmt.Sprintf("%T", ev))
		}
	}
	// Order: submitted, working, artifact (first delta), artifact update (second), completed
	if len(kinds) < 5 {
		t.Fatalf("expected at least 5 events, got %v", kinds)
	}
	if got := kinds[len(kinds)-1]; got != "Status:"+string(a2a.TaskStateCompleted) {
		t.Fatalf("last event should be completed, got %q kinds=%v", got, kinds)
	}
}

func TestAgentA2AExecutor_Execute_Streaming_SkipEmptyDelta(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	streamCh2 := make(chan events.AgentEvent, 4)
	streamCh2 <- events.NewAgentTextMessageContentEvent("m1", "")
	streamCh2 <- events.NewAgentTextMessageContentEvent("m1", "z")
	streamCh2 <- events.NewAgentRunFinishedEvent("", "", nil)
	close(streamCh2)
	var streamRecv2 <-chan events.AgentEvent = streamCh2
	expectRuntimeStream(ctrl, mockRT, streamRecv2, nil)

	a := testAgentWithRuntime(mockRT)
	a.LLMClient = testStreamLLM(t)
	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("p")),
		TaskID:  a2a.NewTaskID(),
	}
	n := 0
	for range exec.Execute(context.Background(), execCtx) {
		n++
	}
	if n < 4 {
		t.Fatalf("expected stream events including empty-delta skip path, got count %d", n)
	}
}

func TestAgentA2AExecutor_Execute_Streaming_StreamOpenError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	expectRuntimeStream(ctrl, mockRT, nil, errors.New("no stream"))

	a := testAgentWithRuntime(mockRT)
	a.LLMClient = testStreamLLM(t)
	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("p")),
		TaskID:  a2a.NewTaskID(),
	}

	for ev, err := range exec.Execute(context.Background(), execCtx) {
		if err != nil {
			t.Fatal(err)
		}
		su, ok := ev.(*a2a.TaskStatusUpdateEvent)
		if ok && su.Status.State == a2a.TaskStateFailed {
			return
		}
	}
	t.Fatal("expected failed status after Stream error")
}

func TestAgentA2AExecutor_Execute_Streaming_RunErrorEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	errCh := make(chan events.AgentEvent, 2)
	errCh <- events.NewAgentRunErrorEvent("boom", "CODE")
	close(errCh)
	var errRecv <-chan events.AgentEvent = errCh
	expectRuntimeStream(ctrl, mockRT, errRecv, nil)

	a := testAgentWithRuntime(mockRT)
	a.LLMClient = testStreamLLM(t)
	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("p")),
		TaskID:  a2a.NewTaskID(),
	}

	for ev, err := range exec.Execute(context.Background(), execCtx) {
		if err != nil {
			t.Fatal(err)
		}
		if su, ok := ev.(*a2a.TaskStatusUpdateEvent); ok && su.Status.State == a2a.TaskStateFailed {
			return
		}
	}
	t.Fatal("expected failed status from run error event")
}

func TestAgentA2AExecutor_Execute_Streaming_CloseWithoutRunFinished(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	closedCh := make(chan events.AgentEvent, 1)
	close(closedCh)
	var closedRecv <-chan events.AgentEvent = closedCh
	expectRuntimeStream(ctrl, mockRT, closedRecv, nil)

	a := testAgentWithRuntime(mockRT)
	a.LLMClient = testStreamLLM(t)
	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("p")),
		TaskID:  a2a.NewTaskID(),
	}

	var last a2a.TaskState
	for ev, err := range exec.Execute(context.Background(), execCtx) {
		if err != nil {
			t.Fatal(err)
		}
		if su, ok := ev.(*a2a.TaskStatusUpdateEvent); ok {
			last = su.Status.State
		}
	}
	if last != a2a.TaskStateCompleted {
		t.Fatalf("want completed fallback when channel empty, got %v", last)
	}
}

func TestAgentA2AExecutor_Cancel(t *testing.T) {
	a := testAgentWithRuntime(nil)
	exec := &agentA2AExecutor{agent: a}
	execCtx := &a2asrv.ExecutorContext{TaskID: a2a.NewTaskID()}
	for ev, err := range exec.Cancel(context.Background(), execCtx) {
		if err != nil {
			t.Fatal(err)
		}
		su, ok := ev.(*a2a.TaskStatusUpdateEvent)
		if !ok || su.Status.State != a2a.TaskStateCanceled {
			t.Fatalf("want canceled, got %T %v", ev, ev)
		}
	}
}

func TestBearerAuthInterceptor(t *testing.T) {
	t.Parallel()
	b := &bearerAuthInterceptor{tokens: []string{"good-token", "other"}}

	t.Run("valid token sets user", func(t *testing.T) {
		ctx, callCtx := a2asrv.NewCallContext(context.Background(), a2asrv.NewServiceParams(map[string][]string{
			"Authorization": {"Bearer good-token"},
		}))
		_, _, err := b.Before(ctx, callCtx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if callCtx.User == nil {
			t.Fatal("expected authenticated user")
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		ctx, callCtx := a2asrv.NewCallContext(context.Background(), a2asrv.NewServiceParams(map[string][]string{
			"Authorization": {"Bearer bad"},
		}))
		_, _, err := b.Before(ctx, callCtx, nil)
		if !errors.Is(err, a2a.ErrUnauthenticated) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("missing auth", func(t *testing.T) {
		ctx, callCtx := a2asrv.NewCallContext(context.Background(), a2asrv.NewServiceParams(nil))
		_, _, err := b.Before(ctx, callCtx, nil)
		if !errors.Is(err, a2a.ErrUnauthenticated) {
			t.Fatalf("got %v", err)
		}
	})

	if err := (&bearerAuthInterceptor{}).After(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunA2A_ServesAgentCardAndSendMessage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	expectRuntimeRun(ctrl, mockRT, &types.AgentRunResult{Content: "hello-from-agent"}, nil)

	a := &Agent{
		agentConfig: agentConfig{
			Name:             "IntegrationA2A",
			Description:      "integration test agent",
			logger:           logger.DefaultLogger("error"),
			tracer:           observability.DefaultNoopTracer,
			metrics:          observability.DefaultNoopMetrics,
			LLMClient:        testLLM(t),
			maxSubAgentDepth: 2,
			a2aServerConfig:  &A2AServerConfig{Hostname: addr.IP.String(), Port: addr.Port},
		},
		runtime: mockRT,
	}
	mustTestRegistries(t, &a.agentConfig)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		defer close(done)
		runErr <- a.RunA2A(ctx)
	}()

	baseURL := fmt.Sprintf("http://%s:%d", addr.IP.String(), addr.Port)
	waitHTTPReady(t, baseURL+a2asrv.WellKnownAgentCardPath)

	resp, err := http.Get(baseURL + a2asrv.WellKnownAgentCardPath)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		cancel()
		<-done
		t.Fatalf("card status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	if raw["url"] != baseURL {
		t.Fatalf("agent card must include top-level url=%q for inspector compatibility, got %#v", baseURL, raw["url"])
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	if card.Name != "IntegrationA2A" {
		t.Fatalf("card name %q", card.Name)
	}

	cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer ccancel()

	cli, err := a2aclient.NewFromEndpoints(cctx, []*a2a.AgentInterface{
		a2a.NewAgentInterface(baseURL, a2a.TransportProtocolJSONRPC),
	})
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	defer func() { _ = cli.Destroy() }()

	sdkRes, err := cli.SendMessage(cctx, &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
	})
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	msg, ok := sdkRes.(*a2a.Message)
	if !ok || msg == nil {
		cancel()
		<-done
		t.Fatalf("expected *a2a.Message result, got %T", sdkRes)
	}
	if collectMessageText(msg) != "hello-from-agent" {
		t.Fatalf("reply text: got %q", collectMessageText(msg))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunA2A did not exit after cancel")
	}
	if err := <-runErr; err != nil {
		t.Fatalf("RunA2A after shutdown: %v", err)
	}
}

// TestRunA2A_JSONRPCSendStreamingMessage_ReturnsSSE guards JSON-RPC streaming on POST /
// (same path as Inspector / Python client when preferredTransport is JSONRPC). Clients set
// Accept: text/event-stream; the response must be SSE, not application/json (which indicates
// sse.NewWriter failed or the request was routed to the non-streaming JSON-RPC path).
func TestRunA2A_JSONRPCSendStreamingMessage_ReturnsSSE(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRT := rtmocks.NewMockRuntime(ctrl)
	expectRuntimeRun(ctrl, mockRT, &types.AgentRunResult{Content: "hello-from-agent"}, nil)

	a := &Agent{
		agentConfig: agentConfig{
			Name:             "IntegrationA2ASSE",
			Description:      "integration test agent",
			logger:           logger.DefaultLogger("error"),
			tracer:           observability.DefaultNoopTracer,
			metrics:          observability.DefaultNoopMetrics,
			LLMClient:        testLLM(t),
			maxSubAgentDepth: 2,
			a2aServerConfig:  &A2AServerConfig{Hostname: addr.IP.String(), Port: addr.Port},
		},
		runtime: mockRT,
	}
	mustTestRegistries(t, &a.agentConfig)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		defer close(done)
		runErr <- a.RunA2A(ctx)
	}()

	baseURL := fmt.Sprintf("http://%s:%d", addr.IP.String(), addr.Port)
	waitHTTPReady(t, baseURL+a2asrv.WellKnownAgentCardPath)

	params, err := json.Marshal(&a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
	})
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "SendStreamingMessage",
		"params":  json.RawMessage(params),
	})
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/", bytes.NewReader(payload))
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		cancel()
		<-done
		t.Fatalf("unexpected status %d, Content-Type=%q, body=%s", resp.StatusCode, ct, body)
	}
	if !strings.Contains(ct, "text/event-stream") {
		body, _ := io.ReadAll(resp.Body)
		cancel()
		<-done
		t.Fatalf("JSON-RPC SendStreamingMessage must return SSE (Content-Type text/event-stream); got %q, body=%s", ct, body)
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunA2A did not exit after cancel")
	}
	if err := <-runErr; err != nil {
		t.Fatalf("RunA2A after shutdown: %v", err)
	}
}

func waitHTTPReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}

// ---------------------------------------------------------------------------
// agentCardProducer tests
// ---------------------------------------------------------------------------

func TestAgentCardProducer_CardJSON_InjectsURLField(t *testing.T) {
	a := &Agent{
		agentConfig: agentConfig{
			Name:            "TestAgent",
			Description:     "test",
			logger:          logger.DefaultLogger("error"),
			a2aServerConfig: &A2AServerConfig{Hostname: "localhost", Port: 8080},
		},
	}
	mustTestRegistries(t, &a.agentConfig)
	p := (*agentCardProducer)(a)
	b, err := p.CardJSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if got := m["url"]; got != "http://localhost:8080" {
		t.Fatalf("expected url=http://localhost:8080, got %v", got)
	}
	if m["name"] != "TestAgent" {
		t.Fatalf("expected name=TestAgent, got %v", m["name"])
	}
}

func TestAgentCardProducer_Card_ReturnsTypedCard(t *testing.T) {
	a := &Agent{
		agentConfig: agentConfig{
			Name:            "TestAgent",
			logger:          logger.DefaultLogger("error"),
			a2aServerConfig: &A2AServerConfig{Hostname: "localhost", Port: 8080},
		},
	}
	mustTestRegistries(t, &a.agentConfig)
	p := (*agentCardProducer)(a)
	card, err := p.Card(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}
	if card.Name != "TestAgent" {
		t.Fatalf("expected card.Name=TestAgent, got %q", card.Name)
	}
}

func TestAgentCardHandler_MethodNotAllowed(t *testing.T) {
	a := &Agent{
		agentConfig: agentConfig{
			Name:            "TestAgent",
			logger:          logger.DefaultLogger("error"),
			a2aServerConfig: &A2AServerConfig{Hostname: "localhost", Port: 8080},
		},
	}
	mustTestRegistries(t, &a.agentConfig)
	srv := httptest.NewServer(a2asrv.NewAgentCardHandler((*agentCardProducer)(a)))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}
