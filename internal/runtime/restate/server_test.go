package restate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterDeployment_UsesDeploymentURL(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rt := testRestateRuntime("a")
	rt.config.Endpoint.AdminURL = srv.URL
	rt.config.Endpoint.DeploymentURL = "http://host.docker.internal:9080"
	rt.httpClient = srv.Client()

	if err := rt.registerDeployment(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, "host.docker.internal:9080") {
		t.Fatalf("body %q missing DeploymentURL", gotBody)
	}
}

func TestRegisterDeployment_ClientErrorNoRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	rt := testRestateRuntime("a")
	rt.config.Endpoint.AdminURL = srv.URL
	rt.httpClient = srv.Client()

	err := rt.registerDeployment(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected single attempt, got %d", calls)
	}
}
