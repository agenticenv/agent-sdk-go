package wikipedia

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// testServerHost returns the "host:port" part of an httptest server URL for use as
// Wikipedia.lang with baseURL "http://%s", so fmt.Sprintf(baseURL, lang) matches srv.URL.
func testServerHost(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestWikipedia_NameDescriptionParameters(t *testing.T) {
	w := New()
	if w.Name() != "wikipedia" {
		t.Errorf("Name = %q", w.Name())
	}
	if w.Description() == "" {
		t.Error("Description empty")
	}
	p := w.Parameters()
	if p["type"] != "object" {
		t.Fatalf("type = %v", p["type"])
	}
	props, ok := p["properties"].(map[string]interfaces.JSONSchema)
	if !ok || props["query"] == nil {
		t.Fatal("expected query property")
	}
}

func TestWikipedia_Execute_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/w/rest.php/v1/search/page") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("expected User-Agent")
		}
		body := `{"pages":[{"id":1,"title":"Go","key":"Go_(programming_language)","excerpt":"Go is ...","thumbnail":{"url":"//upload.example/img.png"},"description":"A programming language"}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	w := &Wikipedia{
		client:  srv.Client(),
		baseURL: "http://%s",
		lang:    testServerHost(t, srv),
		limit:   5,
	}

	got, err := w.Execute(context.Background(), map[string]any{"query": "golang"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %T", got)
	}
	results, ok := m["results"].([]map[string]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v", m["results"])
	}
	row := results[0]
	if row["title"] != "Go" || row["excerpt"] != "Go is ..." {
		t.Fatalf("row = %#v", row)
	}
	if row["description"] != "A programming language" {
		t.Fatalf("description = %v", row["description"])
	}
	if row["thumbnail"] != "https://upload.example/img.png" {
		t.Fatalf("thumbnail = %v", row["thumbnail"])
	}
	wantURL := srv.URL + "/wiki/" + url.PathEscape("Go_(programming_language)")
	if row["url"] != wantURL {
		t.Fatalf("url = %q want %q", row["url"], wantURL)
	}
}

func TestWikipedia_Execute_RequiresQuery(t *testing.T) {
	w := &Wikipedia{client: http.DefaultClient, baseURL: "http://%s", lang: "unused", limit: 5}
	_, err := w.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("got %v", err)
	}
}

func TestWikipedia_Execute_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer srv.Close()
	w := &Wikipedia{client: srv.Client(), baseURL: "http://%s", lang: testServerHost(t, srv), limit: 5}
	_, err := w.Execute(context.Background(), map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("got %v", err)
	}
}

func TestWikipedia_Execute_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not-json")
	}))
	defer srv.Close()
	w := &Wikipedia{client: srv.Client(), baseURL: "http://%s", lang: testServerHost(t, srv), limit: 5}
	_, err := w.Execute(context.Background(), map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "wikipedia decode") {
		t.Fatalf("got %v", err)
	}
}

func TestWikipedia_Execute_LimitArgAndMinimalPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "limit=3") {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"pages":[{"id":2,"title":"T","key":"K","excerpt":"E"}]}`)
	}))
	defer srv.Close()
	w := &Wikipedia{client: srv.Client(), baseURL: "http://%s", lang: testServerHost(t, srv), limit: 5}
	got, err := w.Execute(context.Background(), map[string]any{"query": "q", "limit": float64(3)})
	if err != nil {
		t.Fatal(err)
	}
	results := got.(map[string]any)["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("len = %d", len(results))
	}
	if _, has := results[0]["description"]; has {
		t.Fatal("expected no description")
	}
	if _, has := results[0]["thumbnail"]; has {
		t.Fatal("expected no thumbnail")
	}
}

func TestWikipedia_New_WithLanguageAndLimit(t *testing.T) {
	w := New(WithLanguage("de"), WithLimit(7))
	if w.lang != "de" || w.limit != 7 {
		t.Fatalf("lang=%q limit=%d", w.lang, w.limit)
	}
	w2 := New(WithLanguage(""), WithLimit(0), WithLimit(99))
	if w2.lang != "en" || w2.limit != 5 {
		t.Fatalf("empty/zero/out-of-range opts: lang=%q limit=%d", w2.lang, w2.limit)
	}
}

func TestToolsToInt(t *testing.T) {
	if n, ok := toolsToInt(float64(4)); !ok || n != 4 {
		t.Fatalf("float64")
	}
	if n, ok := toolsToInt(2); !ok || n != 2 {
		t.Fatalf("int")
	}
	if n, ok := toolsToInt(int64(8)); !ok || n != 8 {
		t.Fatalf("int64")
	}
	if _, ok := toolsToInt("x"); ok {
		t.Fatal("string")
	}
}
