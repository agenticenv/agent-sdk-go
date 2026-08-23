package restate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	restateingress "github.com/restatedev/sdk-go/ingress"
)

func TestRunHandle_ID(t *testing.T) {
	h := newRunHandle("run-1", "inv-1", nil)
	if h.ID() != "run-1" {
		t.Fatalf("got %q", h.ID())
	}
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed")
	}
}

func TestRunHandle_EnsureReady(t *testing.T) {
	h := &runHandle{id: "r"}
	if err := h.ensureReady(); err == nil || !strings.Contains(err.Error(), "no runtime") {
		t.Fatalf("got %v", err)
	}
	h.rt = &RestateRuntime{}
	if err := h.ensureReady(); err == nil || !strings.Contains(err.Error(), "no invocationId") {
		t.Fatalf("got %v", err)
	}
	h.invocationID = "inv"
	if err := h.ensureReady(); err == nil || !strings.Contains(err.Error(), "ingress") {
		t.Fatalf("got %v", err)
	}
}

func TestRunHandle_Cancel_RequiresIngress(t *testing.T) {
	h := &runHandle{
		id:           "r",
		invocationID: "inv",
		rt:           &RestateRuntime{},
		doneCh:       make(chan struct{}),
	}
	err := h.Cancel(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ingress") {
		t.Fatalf("got %v", err)
	}
}

func TestRunHandle_Get_ContextCanceled(t *testing.T) {
	h := &runHandle{id: "r", doneCh: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.Get(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestRunHandle_Get_AfterDone(t *testing.T) {
	h := &runHandle{id: "r", doneCh: make(chan struct{})}
	want := &types.AgentRunResult{Content: "hi", RunID: "r"}
	h.res = want
	close(h.doneCh)
	got, err := h.Get(context.Background())
	if err != nil || got != want {
		t.Fatalf("got %#v err=%v", got, err)
	}
}

func TestRunHandle_Status(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"result":{"content":"ok"}}`))
		}))
		t.Cleanup(srv.Close)
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.httpClient = srv.Client()
		rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
		h := &runHandle{id: "r", invocationID: "inv", rt: rt, doneCh: make(chan struct{})}
		st, err := h.Status(context.Background())
		if err != nil || st != types.StatusCompleted {
			t.Fatalf("got %q err=%v", st, err)
		}
	})
	t.Run("running", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(470)
		}))
		t.Cleanup(srv.Close)
		rt := testRestateRuntime("a")
		rt.config.Ingress.URL = srv.URL
		rt.httpClient = srv.Client()
		rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
		h := &runHandle{id: "r", invocationID: "inv", rt: rt, doneCh: make(chan struct{})}
		st, err := h.Status(context.Background())
		if err != nil || st != types.StatusRunning {
			t.Fatalf("got %q err=%v", st, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		rt := testRestateRuntime("a")
		rt.ingressClient = restateingress.NewClient("http://127.0.0.1:1")
		h := &runHandle{id: "r", invocationID: "inv", rt: rt, doneCh: make(chan struct{}), cancelled: true}
		st, err := h.Status(context.Background())
		if err != nil || st != types.StatusCancelled {
			t.Fatalf("got %q err=%v", st, err)
		}
	})
}

func TestRunHandle_Cancel_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`null`))
	}))
	t.Cleanup(srv.Close)
	rt := testRestateRuntime("a")
	rt.config.Ingress.URL = srv.URL
	rt.httpClient = srv.Client()
	rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
	h := &runHandle{id: "r", invocationID: "inv_1", rt: rt, doneCh: make(chan struct{})}
	if err := h.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Cancel(context.Background()); !errors.Is(err, types.ErrRunAlreadyCompleted) {
		t.Fatalf("got %v", err)
	}
}

func TestRunHandle_AwaitCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"content":"done"}}`))
	}))
	t.Cleanup(srv.Close)
	rt := testRestateRuntime("a")
	rt.config.Ingress.URL = srv.URL
	rt.httpClient = srv.Client()
	rt.ingressClient = restateingress.NewClient(srv.URL, restateingress.WithHttpClient(srv.Client()))
	h := newRunHandle("run-x", "inv_x", rt)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := h.Get(ctx)
	if err != nil || res.Content != "done" || res.RunID != "run-x" {
		t.Fatalf("got %#v err=%v", res, err)
	}
}

func TestMapInvocationError_BudgetExceeded(t *testing.T) {
	raw := errors.New("invocation failed: agent: per-run budget exceeded: total tokens 76 exceeds limit 50")
	if !errors.Is(mapInvocationError(raw), types.ErrBudgetExceeded) {
		t.Fatalf("got %v", mapInvocationError(raw))
	}
	if !errors.Is(mapInvocationError(types.ErrBudgetExceeded), types.ErrBudgetExceeded) {
		t.Fatal("sentinel not preserved")
	}
	unavail := errors.New("agent: budget approval unavailable (stream not connected): no subscriber")
	if !errors.Is(mapInvocationError(unavail), types.ErrBudgetApprovalUnavailable) {
		t.Fatalf("got %v", mapInvocationError(unavail))
	}
	other := errors.New("connection refused")
	if mapInvocationError(other) != other {
		t.Fatalf("got %v", mapInvocationError(other))
	}
	if mapInvocationError(nil) != nil {
		t.Fatal("nil should stay nil")
	}
}
