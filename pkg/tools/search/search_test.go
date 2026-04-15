package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestSearch_NameDescriptionApproval(t *testing.T) {
	s := New(WithAPIKey("k"))
	if s.Name() != "search" {
		t.Errorf("Name() = %q", s.Name())
	}
	if s.Description() == "" {
		t.Error("Description empty")
	}
	if !s.ApprovalRequired() {
		t.Error("expected approval required")
	}
}

func TestSearch_Parameters(t *testing.T) {
	s := New(WithAPIKey("k"))
	p := s.Parameters()
	if p["type"] != "object" {
		t.Fatalf("type = %v", p["type"])
	}
	props, ok := p["properties"].(map[string]interfaces.JSONSchema)
	if !ok || props["query"] == nil {
		t.Fatal("expected query property")
	}
}

func TestSearch_Execute_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.Header.Get("X-API-KEY") != "secret" {
			t.Errorf("X-API-KEY = %q", r.Header.Get("X-API-KEY"))
		}
		body, _ := io.ReadAll(r.Body)
		var req serperReq
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("body: %v", err)
		}
		if req.Q != "golang httptest" || req.Num != 3 {
			t.Fatalf("req = %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(serperResp{
			Organic: []serperOrg{
				{Title: "T1", Link: "https://a.example", Snippet: "S1"},
			},
		})
	}))
	defer srv.Close()

	s := &Search{
		client:  srv.Client(),
		apiKey:  "secret",
		baseURL: srv.URL,
	}

	got, err := s.Execute(context.Background(), map[string]any{
		"query": "golang httptest",
		"num":   3,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got type %T", got)
	}
	raw, ok := out["results"].([]map[string]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("results = %#v", out["results"])
	}
	if raw[0]["title"] != "T1" || raw[0]["link"] != "https://a.example" || raw[0]["snippet"] != "S1" {
		t.Fatalf("row = %#v", raw[0])
	}
}

func TestSearch_Execute_DefaultNum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req serperReq
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.Num != 5 {
			t.Fatalf("default num = %d, want 5", req.Num)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(serperResp{})
	}))
	defer srv.Close()

	s := &Search{client: srv.Client(), apiKey: "k", baseURL: srv.URL}
	_, err := s.Execute(context.Background(), map[string]any{"query": "q"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSearch_Execute_RequiresAPIKey(t *testing.T) {
	s := &Search{client: http.DefaultClient, baseURL: "http://unused"}
	_, err := s.Execute(context.Background(), map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "SERPER_API_KEY") {
		t.Fatalf("got %v", err)
	}
}

func TestSearch_Execute_RequiresQuery(t *testing.T) {
	s := &Search{client: http.DefaultClient, apiKey: "k", baseURL: "http://unused"}
	_, err := s.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("got %v", err)
	}
}

func TestSearch_Execute_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream", http.StatusBadGateway)
	}))
	defer srv.Close()

	s := &Search{client: srv.Client(), apiKey: "k", baseURL: srv.URL}
	_, err := s.Execute(context.Background(), map[string]any{"query": "q"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("got %v", err)
	}
}

func TestSearch_Execute_InvalidJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	s := &Search{client: srv.Client(), apiKey: "k", baseURL: srv.URL}
	_, err := s.Execute(context.Background(), map[string]any{"query": "q"})
	if err == nil || !strings.Contains(err.Error(), "search decode") {
		t.Fatalf("got %v", err)
	}
}

func TestToInt(t *testing.T) {
	if n, ok := toInt(float64(7)); !ok || n != 7 {
		t.Fatalf("float64: %v %v", n, ok)
	}
	if n, ok := toInt(4); !ok || n != 4 {
		t.Fatalf("int: %v %v", n, ok)
	}
	if n, ok := toInt(int64(9)); !ok || n != 9 {
		t.Fatalf("int64: %v %v", n, ok)
	}
	if _, ok := toInt("x"); ok {
		t.Fatal("string should not convert")
	}
}

func TestSearch_New_WithAPIKey(t *testing.T) {
	s := New(WithAPIKey("from-opt"))
	if s.apiKey != "from-opt" {
		t.Fatalf("apiKey = %q", s.apiKey)
	}
}
