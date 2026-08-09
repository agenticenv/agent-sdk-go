package restate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	restateingress "github.com/restatedev/sdk-go/ingress"
)

func TestIsRetryableHTTPStatus(t *testing.T) {
	for code, want := range map[int]bool{
		http.StatusOK:                  false,
		http.StatusBadRequest:          false,
		http.StatusTooManyRequests:     true,
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
		http.StatusGatewayTimeout:      true,
		http.StatusInternalServerError: false,
	} {
		if got := isRetryableHTTPStatus(code); got != want {
			t.Fatalf("code %d: got %v want %v", code, got, want)
		}
	}
}

func TestIsTransientIngressErr(t *testing.T) {
	if isTransientIngressErr(nil) {
		t.Fatal("nil should not be transient")
	}
	if !isTransientIngressErr(&retryableHTTPError{status: 503, body: "x"}) {
		t.Fatal("retryableHTTPError should be transient")
	}
	if !isTransientIngressErr(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should be transient")
	}
	if !isTransientIngressErr(&net.OpError{Op: "dial", Err: errors.New("refused")}) {
		t.Fatal("net.OpError should be transient")
	}
	if isTransientIngressErr(errors.New("permanent")) {
		t.Fatal("generic error should not be transient")
	}
}

func TestRetryableHTTPError_Error(t *testing.T) {
	err := &retryableHTTPError{status: 429, body: "slow down"}
	got := err.Error()
	if !strings.Contains(got, "429") || !strings.Contains(got, "slow down") {
		t.Fatalf("Error(): %q", got)
	}
}

func TestWithIngressRetry_successFirstAttempt(t *testing.T) {
	rt := &RestateRuntime{config: &RestateConfig{
		Ingress: IngressConfig{HTTPTimeout: time.Second, HTTPMaxAttempts: 3},
	}}
	var calls atomic.Int32
	got, err := withIngressRetry(context.Background(), rt, func(context.Context) (string, error) {
		calls.Add(1)
		return "ok", nil
	})
	if err != nil || got != "ok" || calls.Load() != 1 {
		t.Fatalf("got %q err=%v calls=%d", got, err, calls.Load())
	}
}

func TestWithIngressRetry_retriesThenSucceeds(t *testing.T) {
	rt := &RestateRuntime{config: &RestateConfig{
		Ingress: IngressConfig{HTTPTimeout: time.Second, HTTPMaxAttempts: 3},
	}}
	var calls atomic.Int32
	got, err := withIngressRetry(context.Background(), rt, func(context.Context) (string, error) {
		n := calls.Add(1)
		if n < 3 {
			return "", &retryableHTTPError{status: 503, body: "busy"}
		}
		return "ok", nil
	})
	if err != nil || got != "ok" || calls.Load() != 3 {
		t.Fatalf("got %q err=%v calls=%d", got, err, calls.Load())
	}
}

func TestWithIngressRetry_permanentStops(t *testing.T) {
	rt := &RestateRuntime{config: &RestateConfig{
		Ingress: IngressConfig{HTTPTimeout: time.Second, HTTPMaxAttempts: 5},
	}}
	var calls atomic.Int32
	_, err := withIngressRetry(context.Background(), rt, func(context.Context) (int, error) {
		calls.Add(1)
		return 0, errors.New("nope")
	})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestWithIngressRetry_parentCancel(t *testing.T) {
	rt := &RestateRuntime{config: &RestateConfig{
		Ingress: IngressConfig{HTTPTimeout: time.Second, HTTPMaxAttempts: 5},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := withIngressRetry(ctx, rt, func(context.Context) (int, error) {
		return 0, &retryableHTTPError{status: 503, body: "x"}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestNewIngressHTTPClient(t *testing.T) {
	c := newIngressHTTPClient(nil)
	if c == nil || c.Transport == nil {
		t.Fatal("expected client with transport")
	}
}

func TestDoIngressHTTP_SuccessAndRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("busy"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	rt := testRestateRuntime("a")
	rt.config.Ingress.HTTPMaxAttempts = 3
	rt.config.Ingress.HTTPTimeout = time.Second
	rt.httpClient = srv.Client()

	resp, body, err := rt.doIngressHTTP(context.Background(), func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") || calls != 2 {
		t.Fatalf("status=%d body=%s calls=%d", resp.StatusCode, body, calls)
	}
}

func TestDoIngressHTTP_NoClient(t *testing.T) {
	rt := testRestateRuntime("a")
	rt.httpClient = nil
	_, _, err := rt.doIngressHTTP(context.Background(), func(context.Context) (*http.Request, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "http client") {
		t.Fatalf("got %v", err)
	}
}

func TestApprove_HTTP(t *testing.T) {
	for _, tc := range []struct {
		status int
		ok     bool
		is     error
	}{
		{http.StatusOK, true, nil},
		{http.StatusAccepted, true, nil},
		{http.StatusNoContent, true, nil},
		{http.StatusNotFound, false, types.ErrApprovalAlreadyResolved},
		{http.StatusConflict, false, types.ErrApprovalAlreadyResolved},
		{http.StatusBadRequest, false, nil},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.Path, "/restate/awakeables/") {
				t.Fatalf("path %s", r.URL.Path)
			}
			w.WriteHeader(tc.status)
		}))
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.config.Ingress.AuthKey = "k"
		rt.httpClient = srv.Client()
		err := rt.approve(context.Background(), "tok", types.ApprovalStatusApproved)
		srv.Close()
		if tc.ok {
			if err != nil {
				t.Fatalf("status %d: %v", tc.status, err)
			}
			continue
		}
		if tc.is != nil {
			if !errors.Is(err, tc.is) {
				t.Fatalf("status %d: got %v", tc.status, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("status %d: expected error", tc.status)
		}
	}
}

func TestLookupInvocationID(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/restate/lookup" {
				t.Fatalf("path %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"invocationId":"inv_99"}`))
		}))
		t.Cleanup(srv.Close)
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.httpClient = srv.Client()
		id, err := rt.lookupInvocationID(context.Background(), agentLoopRunHandler, "run-abc")
		if err != nil || id != "inv_99" {
			t.Fatalf("got %q err=%v", id, err)
		}
	})
	t.Run("notFound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.httpClient = srv.Client()
		_, err := rt.lookupInvocationID(context.Background(), agentLoopRunHandler, "gone")
		if !errors.Is(err, types.ErrRunNotFound) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSendAgentLoop(t *testing.T) {
	rt := testRestateRuntime("a")
	if _, _, err := rt.sendAgentLoop(context.Background(), nil, agentLoopRunHandler, false); err == nil {
		t.Fatal("expected nil request error")
	}

	var gotReq AgentLoopRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/send") {
			t.Fatalf("path %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"invocationId":"inv_send","status":"Accepted"}`))
	}))
	t.Cleanup(srv.Close)
	rt.config.Ingress.URL = srv.URL
	rt.httpClient = srv.Client()
	rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))

	// Memory scope must be resolved on the client ctx (tenant/user) before ingress Send.
	memCfg := memory.DefaultConfig(&stubMemory{})
	rt.AgentConfig.Memory = sdkruntime.AgentMemory{Config: &memCfg}
	ctx := memory.WithContextUserID(
		memory.WithContextTenantID(context.Background(), "tenant-demo"),
		"user-demo",
	)

	runID, invID, err := rt.sendAgentLoop(ctx, &sdkruntime.RunRequest{
		UserPrompt: "hi",
		EventTypes: []events.AgentEventType{events.AgentEventAll},
	}, agentLoopRunHandler, false)
	if err != nil || runID == "" || invID != "inv_send" {
		t.Fatalf("runID=%q invID=%q err=%v", runID, invID, err)
	}
	if gotReq.MemoryScope.TenantID != "tenant-demo" || gotReq.MemoryScope.UserID != "user-demo" {
		t.Fatalf("MemoryScope not forwarded: %+v", gotReq.MemoryScope)
	}
}

// stubMemory satisfies interfaces.Memory for ingress tests (no I/O).
type stubMemory struct{}

func (stubMemory) Store(context.Context, interfaces.MemoryScope, interfaces.MemoryRecord, ...interfaces.StoreMemoryOption) (string, error) {
	return "", nil
}
func (stubMemory) Load(context.Context, interfaces.MemoryScope, string, ...interfaces.LoadMemoryOption) ([]interfaces.MemoryEntry, error) {
	return nil, nil
}
func (stubMemory) Clear(context.Context, interfaces.MemoryScope) error { return nil }
func (stubMemory) Delete(context.Context, interfaces.MemoryScope, string) error {
	return nil
}
func (stubMemory) Close() error { return nil }

func TestCancelInvocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, agentLoopCancelHandler) {
			t.Fatalf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`null`))
	}))
	t.Cleanup(srv.Close)
	rt := testRestateRuntime("a")
	rt.config.Ingress.URL = srv.URL
	rt.httpClient = srv.Client()
	rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
	if err := rt.cancelInvocation(context.Background(), "run", "inv_1"); err != nil {
		t.Fatal(err)
	}
	if err := rt.cancelInvocation(context.Background(), "run", "  "); err == nil {
		t.Fatal("expected empty invocationId error")
	}
}

func TestRunApprovalLoop_NilExits(t *testing.T) {
	rt := testRestateRuntime("a")
	h := &runHandle{id: "r", doneCh: make(chan struct{})}
	close(h.doneCh)
	rt.runApprovalLoop(context.Background(), "r", h)
	rt.runApprovalLoop(context.Background(), "r", nil)
}

func TestRegisterDeployment_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/deployments" {
			t.Fatalf("path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	rt := testRestateRuntime("a")
	rt.config.Endpoint.AdminURL = srv.URL
	rt.httpClient = srv.Client()
	if err := rt.registerDeployment(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := (&RestateRuntime{}).registerDeployment(context.Background()); err != nil {
		t.Fatal(err)
	}
	rtNoAdmin := testRestateRuntime("b")
	rtNoAdmin.config.Endpoint.AdminURL = ""
	if err := rtNoAdmin.registerDeployment(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartEndpoint_Guards(t *testing.T) {
	rt := testRestateRuntime("a")
	rt.endpoint.started = true
	if err := rt.startEndpoint(); err != nil {
		t.Fatal(err)
	}
	rt3 := testRestateRuntime("a")
	if err := rt3.startEndpoint(); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveRunningInvocation(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"result":{"content":"done"}}`))
		}))
		t.Cleanup(srv.Close)
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.httpClient = srv.Client()
		rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
		_, err := rt.resolveRunningInvocation(context.Background(), agentLoopRunHandler, "run")
		if !errors.Is(err, types.ErrRunAlreadyCompleted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("notFound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.httpClient = srv.Client()
		rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
		_, err := rt.resolveRunningInvocation(context.Background(), agentLoopRunHandler, "gone")
		if !errors.Is(err, types.ErrRunNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("running", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/restate/lookup") {
				_, _ = w.Write([]byte(`{"invocationId":"inv_live"}`))
				return
			}
			w.WriteHeader(470)
		}))
		t.Cleanup(srv.Close)
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.httpClient = srv.Client()
		rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
		id, err := rt.resolveRunningInvocation(context.Background(), agentLoopRunHandler, "run-live")
		if err != nil || id != "inv_live" {
			t.Fatalf("got %q err=%v", id, err)
		}
	})
}
