package restate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	restateingress "github.com/restatedev/sdk-go/ingress"
)

func TestEnsureIngress(t *testing.T) {
	rt := &RestateRuntime{}
	if err := rt.ensureIngress(); err == nil || !strings.Contains(err.Error(), "ingress") {
		t.Fatalf("got %v", err)
	}
}

func TestServiceName_AgentName(t *testing.T) {
	rt := testRestateRuntime("agent-1")
	if rt.agentLoopServiceName != "AgentLoop_agent-1" {
		t.Fatalf("loop: got %q", rt.agentLoopServiceName)
	}
	if rt.eventLogServiceName != "AgentEventLog_agent-1" {
		t.Fatalf("event log: got %q", rt.eventLogServiceName)
	}
	if got := (AgentLoop{rt: rt}).ServiceName(); got != "AgentLoop_agent-1" {
		t.Fatalf("AgentLoop.ServiceName: got %q", got)
	}
	if got := (AgentEventLog{name: rt.eventLogServiceName}).ServiceName(); got != "AgentEventLog_agent-1" {
		t.Fatalf("AgentEventLog.ServiceName: got %q", got)
	}
}

func TestIngressHTTPTimeoutAndAttempts(t *testing.T) {
	// Both fields are always set by validateConfig; exercise the direct-field path.
	rt := &RestateRuntime{config: &RestateConfig{Ingress: IngressConfig{
		HTTPTimeout:     defaultIngressHTTPTimeout,
		HTTPMaxAttempts: defaultIngressHTTPAttempts,
	}}}
	if rt.ingressHTTPTimeout() != defaultIngressHTTPTimeout {
		t.Fatalf("default timeout: %v", rt.ingressHTTPTimeout())
	}
	if rt.ingressHTTPAttempts() != defaultIngressHTTPAttempts {
		t.Fatalf("default attempts: %d", rt.ingressHTTPAttempts())
	}
	rt.config = &RestateConfig{Ingress: IngressConfig{
		HTTPTimeout:     5 * time.Second,
		HTTPMaxAttempts: 7,
	}}
	if rt.ingressHTTPTimeout() != 5*time.Second || rt.ingressHTTPAttempts() != 7 {
		t.Fatalf("got timeout=%v attempts=%d", rt.ingressHTTPTimeout(), rt.ingressHTTPAttempts())
	}
}

func TestApprovalTaskTimeout(t *testing.T) {
	rt := testRestateRuntime("a")
	if got := rt.approvalTaskTimeout(); got != types.MaxApprovalTimeout {
		t.Fatalf("default: %v", got)
	}
	rt.AgentConfig.Limits.ApprovalTimeout = 10 * time.Second
	if got := rt.approvalTaskTimeout(); got != 10*time.Second {
		t.Fatalf("got %v", got)
	}
	rt.AgentConfig.Limits.ApprovalTimeout = types.MaxApprovalTimeout + time.Hour
	if got := rt.approvalTaskTimeout(); got != types.MaxApprovalTimeout {
		t.Fatalf("capped: %v", got)
	}
}

func TestToolExecutionPolicy(t *testing.T) {
	rt := testRestateRuntime("a")
	policies := sdkruntime.ResolveExecutionPolicies(sdkruntime.ExecutionConfigs{})
	if p := rt.toolExecutionPolicy(types.ToolKindMCP, policies); p != policies.MCP {
		t.Fatal("MCP policy")
	}
	if p := rt.toolExecutionPolicy(types.ToolKindA2A, policies); p != policies.A2A {
		t.Fatal("A2A policy")
	}
	if p := rt.toolExecutionPolicy(types.ToolKindNative, policies); p != policies.ToolExecute {
		t.Fatal("default policy")
	}
}

func TestSubAgentExecutionPolicy_timeoutFallback(t *testing.T) {
	rt := testRestateRuntime("a")
	if got := rt.subAgentExecutionPolicy().Timeout; got != 0 {
		t.Fatalf("default timeout: got %v", got)
	}

	rt.AgentConfig.Limits.Timeout = 60 * time.Minute
	if got := rt.subAgentExecutionPolicy().Timeout; got != 60*time.Minute {
		t.Fatalf("limits fallback: got %v", got)
	}

	rt.AgentConfig.ExecutionConfigs.SubAgent = sdkruntime.ExecutionConfig{Timeout: 20 * time.Minute}
	if got := rt.subAgentExecutionPolicy().Timeout; got != 20*time.Minute {
		t.Fatalf("explicit override: got %v", got)
	}
}

func TestLoadStagedRun(t *testing.T) {
	rt := testRestateRuntime("a")
	if got := rt.loadStagedRun("missing"); got.tools != nil {
		t.Fatalf("empty: %#v", got)
	}
	rt.tools.stash.Store("r1", stagedRun{
		tools:            nil,
		eventTypes:       []events.AgentEventType{events.AgentEventAll},
		maxSubAgentDepth: 2,
	})
	got := rt.loadStagedRun("r1")
	if got.maxSubAgentDepth != 2 || len(got.eventTypes) != 1 {
		t.Fatalf("got %#v", got)
	}
	// Must survive for Restate replay (not deleted).
	got2 := rt.loadStagedRun("r1")
	if got2.maxSubAgentDepth != 2 {
		t.Fatal("staged run should remain until handle deletes")
	}
}

func TestResolveTools(t *testing.T) {
	rt := testRestateRuntime("a")
	tools, err := rt.resolveTools(context.Background(), "runid")
	if err != nil || tools != nil {
		t.Fatalf("got %v err=%v", tools, err)
	}
	rt.tools.resolver = func(context.Context) ([]interfaces.Tool, error) {
		return []interfaces.Tool{}, nil
	}
	tools, err = rt.resolveTools(context.Background(), "runid")
	if err != nil || tools == nil {
		t.Fatalf("got %v err=%v", tools, err)
	}
}

func TestEndpointSlogHandler(t *testing.T) {
	h := endpointSlogHandler(nil)
	if h == nil {
		t.Fatal("nil logger should still return a handler")
	}
	h = endpointSlogHandler(logger.NoopLogger())
	if h == nil {
		t.Fatal("noop logger handler")
	}
}

func TestOnApproval_EmptyToken(t *testing.T) {
	rt := testRestateRuntime("a")
	err := rt.OnApproval(context.Background(), "  ", types.ApprovalStatusApproved)
	if err == nil || !strings.Contains(err.Error(), "empty approval token") {
		t.Fatalf("got %v", err)
	}
}

func TestApprove_InvalidStatus(t *testing.T) {
	rt := testRestateRuntime("a")
	err := rt.approve(context.Background(), "tok", types.ApprovalStatus("NOPE"))
	if err == nil || !strings.Contains(err.Error(), "invalid approval status") {
		t.Fatalf("got %v", err)
	}
}

func TestStream_ContextCanceled(t *testing.T) {
	rt := testRestateRuntime("a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rt.Stream(ctx, &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err == nil {
		t.Fatal("expected canceled")
	}
}

func TestClose_Idempotent(t *testing.T) {
	rt := testRestateRuntime("a")
	canceled := false
	rt.endpoint.cancel = func() { canceled = true }
	rt.Close()
	if !canceled {
		t.Fatal("expected cancel")
	}
	if rt.endpoint.cancel != nil {
		t.Fatal("cancel should be cleared")
	}
	rt.Close() // no panic
}

func TestDeploymentURLFromListen(t *testing.T) {
	cases := map[string]string{
		"":                                 "http://127.0.0.1:9080",
		":9080":                            "http://127.0.0.1:9080",
		"0.0.0.0:9080":                     "http://0.0.0.0:9080",
		"http://host.docker.internal:9080": "http://host.docker.internal:9080",
	}
	for in, want := range cases {
		if got := deploymentURLFromListen(in); got != want {
			t.Fatalf("in %q: got %q want %q", in, got, want)
		}
	}
}

func TestRegisterDeployment_NoAdminURL(t *testing.T) {
	rt := testRestateRuntime("a")
	rt.config = validRestateConfig()
	if err := rt.registerDeployment(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntime_RunAndStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"invocationId":"inv_r","status":"Accepted"}`))
	}))
	t.Cleanup(srv.Close)

	rt := testRestateRuntime("agent")
	rt.config.Ingress.URL = srv.URL
	rt.httpClient = srv.Client()
	rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))

	h, err := rt.Run(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err != nil || h.ID() == "" {
		t.Fatalf("Run: %v id=%q", err, h)
	}
	sh, err := rt.Stream(context.Background(), &sdkruntime.RunRequest{UserPrompt: "hi"})
	if err != nil || sh.ID() == "" {
		t.Fatalf("Stream: %v", err)
	}
}

func TestRuntime_GetHandles(t *testing.T) {
	rt := testRestateRuntime("a")
	if _, err := rt.GetRunHandle(context.Background(), "  "); !errors.Is(err, types.ErrRunNotFound) {
		t.Fatalf("empty run: %v", err)
	}
	if _, err := rt.GetStreamHandle(context.Background(), ""); !errors.Is(err, types.ErrStreamNotFound) {
		t.Fatalf("empty stream: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/restate/lookup") {
			_, _ = w.Write([]byte(`{"invocationId":"inv_get"}`))
			return
		}
		w.WriteHeader(470)
	}))
	t.Cleanup(srv.Close)
	rt.config.Ingress.URL = srv.URL
	rt.httpClient = srv.Client()
	rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))

	h, err := rt.GetRunHandle(context.Background(), "run-live")
	if err != nil || h.ID() != "run-live" {
		t.Fatalf("GetRunHandle: %v %#v", err, h)
	}
	sh, err := rt.GetStreamHandle(context.Background(), "stream-live")
	if err != nil || sh.ID() != "stream-live" {
		t.Fatalf("GetStreamHandle: %v", err)
	}
}

func TestResolveTools_StashPreferred(t *testing.T) {
	rt := testRestateRuntime("a")
	rt.tools.stash.Store("rid", stagedRun{tools: []interfaces.Tool{}})
	// empty tools in stash falls through to resolver
	rt.tools.resolver = func(context.Context) ([]interfaces.Tool, error) {
		return []interfaces.Tool{}, nil
	}
	got, err := rt.resolveTools(context.Background(), "rid")
	if err != nil || got == nil {
		t.Fatalf("got %v err=%v", got, err)
	}
}
