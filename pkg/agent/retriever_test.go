package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// testCtxKey is an unexported type used as a context key in tests (avoids SA1029 staticcheck).
type testCtxKey struct{}

type retrieverExecuteStub struct {
	name      string
	lastCtx   context.Context
	lastQuery string
	docs      []interfaces.Document
	err       error
}

func (r *retrieverExecuteStub) Name() string { return r.name }

func (r *retrieverExecuteStub) Search(ctx context.Context, query string) ([]interfaces.Document, error) {
	r.lastCtx = ctx
	r.lastQuery = query
	if r.err != nil {
		return nil, r.err
	}
	return r.docs, nil
}

func TestRetrieverToolName(t *testing.T) {
	if got := types.RetrieverToolName("  wiki  "); got != "retriever_wiki" {
		t.Fatalf("got %q", got)
	}
	if types.RetrieverToolName("") != "" || types.RetrieverToolName("  ") != "" {
		t.Fatal("expected empty for missing name")
	}
}

func TestRetrieverToolDisplayName(t *testing.T) {
	if got := types.RetrieverToolDisplayName("wiki"); got != "wiki Retriever Tool" {
		t.Fatalf("got %q", got)
	}
	if types.RetrieverToolDisplayName("  ") != "" {
		t.Fatal("expected empty")
	}
}

func TestNewRetrieverTool_nil(t *testing.T) {
	if NewRetrieverTool(nil) != nil {
		t.Fatal("expected nil")
	}
}

func TestNewRetrieverTool_emptyName(t *testing.T) {
	if NewRetrieverTool(&retrieverExecuteStub{name: "  "}) != nil {
		t.Fatal("expected nil for empty name")
	}
}

func TestRetrieverTool_NilReceiver(t *testing.T) {
	var t0 *RetrieverTool
	if t0.Name() != "" || t0.DisplayName() != "" || t0.Description() != "" {
		t.Fatal("nil receiver should return empty strings")
	}
	p := t0.Parameters()
	if p["type"] != "object" {
		t.Fatalf("Parameters = %v", p)
	}
}

func TestRetrieverToolName_ViaStruct(t *testing.T) {
	tool := NewRetrieverTool(&retrieverExecuteStub{name: "  weaviate  "})
	if tool.Name() != "retriever_weaviate" {
		t.Fatalf("Name = %q", tool.Name())
	}
}

func TestRetrieverTool_NameDisplayDescription(t *testing.T) {
	tool := NewRetrieverTool(&retrieverExecuteStub{name: "weaviate"}).(*RetrieverTool)
	if tool.Name() != "retriever_weaviate" {
		t.Fatalf("Name = %q", tool.Name())
	}
	if tool.DisplayName() != "weaviate Retriever Tool" {
		t.Fatalf("DisplayName = %q", tool.DisplayName())
	}
	if !strings.Contains(tool.Description(), "weaviate") {
		t.Fatalf("Description = %q", tool.Description())
	}
}

func TestRetrieverTool_Parameters(t *testing.T) {
	tool := NewRetrieverTool(&retrieverExecuteStub{name: "kb"})
	p := tool.Parameters()
	if p["type"] != "object" {
		t.Fatalf("type = %v", p["type"])
	}
	req, ok := p["required"].([]string)
	if !ok || len(req) != 1 || req[0] != types.RetrieverToolParamQuery {
		t.Fatalf("required = %v", p["required"])
	}
}

func TestRetrieverTool_Execute_success(t *testing.T) {
	stub := &retrieverExecuteStub{
		name: "kb",
		docs: []interfaces.Document{
			{Content: "Go is great", Source: "doc1.md", Score: 0.9},
			{Content: "Rust is fast", Source: "doc2.md", Score: 0.8},
		},
	}
	tool := NewRetrieverTool(stub)
	ctx := context.WithValue(context.Background(), testCtxKey{}, "marker")

	out, err := tool.Execute(ctx, map[string]any{types.RetrieverToolParamQuery: "  golang  "})
	if err != nil {
		t.Fatal(err)
	}
	docs, ok := out.([]interfaces.Document)
	if !ok {
		t.Fatalf("got %T", out)
	}
	if len(docs) != 2 || docs[0].Content != "Go is great" || docs[1].Content != "Rust is fast" {
		t.Fatalf("docs = %#v", docs)
	}
	if stub.lastQuery != "golang" {
		t.Fatalf("query = %q", stub.lastQuery)
	}
	if stub.lastCtx != ctx {
		t.Fatal("Search did not receive Execute context")
	}
}

func TestRetrieverTool_Execute_noDocs(t *testing.T) {
	tool := NewRetrieverTool(&retrieverExecuteStub{name: "kb"})
	out, err := tool.Execute(context.Background(), map[string]any{types.RetrieverToolParamQuery: "x"})
	if err != nil {
		t.Fatal(err)
	}
	docs, ok := out.([]interfaces.Document)
	if !ok {
		t.Fatalf("got %T", out)
	}
	if len(docs) != 0 {
		t.Fatalf("docs = %#v", docs)
	}
}

func TestRetrieverTool_Execute_missingQuery(t *testing.T) {
	tool := NewRetrieverTool(&retrieverExecuteStub{name: "kb"})
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), types.RetrieverToolParamQuery) {
		t.Fatalf("got %v", err)
	}
}

func TestRetrieverTool_Execute_emptyQuery(t *testing.T) {
	tool := NewRetrieverTool(&retrieverExecuteStub{name: "kb"})
	_, err := tool.Execute(context.Background(), map[string]any{types.RetrieverToolParamQuery: "   "})
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("got %v", err)
	}
}

func TestRetrieverTool_Execute_searchError(t *testing.T) {
	want := errors.New("search failed")
	tool := NewRetrieverTool(&retrieverExecuteStub{name: "kb", err: want})
	_, err := tool.Execute(context.Background(), map[string]any{types.RetrieverToolParamQuery: "q"})
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestRetrieverTool_Execute_nilRetriever(t *testing.T) {
	tool := &RetrieverTool{RetrieverName: "kb", Retriever: nil}
	_, err := tool.Execute(context.Background(), map[string]any{types.RetrieverToolParamQuery: "q"})
	if err == nil || !strings.Contains(err.Error(), "nil retriever") {
		t.Fatalf("got %v", err)
	}
}

// ---------------------------------------------------------------------------
// retrieverConfigFingerprint tests

func TestRetrieverConfigFingerprint_nilEntriesIgnored(t *testing.T) {
	r := &retrieverExecuteStub{name: "kb"}
	// A list with nil entries must produce the same fingerprint as the list without them.
	fpClean := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{r})
	fpNils := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{nil, r, nil})
	if fpClean != fpNils {
		t.Fatalf("nil entries must not affect the fingerprint: %q vs %q", fpClean, fpNils)
	}
}

func TestRetrieverConfigFingerprint_duplicateNamesIgnored(t *testing.T) {
	r1 := &retrieverExecuteStub{name: "kb"}
	r2 := &retrieverExecuteStub{name: "kb"}
	fpOne := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{r1})
	fpDup := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{r1, r2})
	if fpOne != fpDup {
		t.Fatalf("duplicate retriever names must not affect the fingerprint: %q vs %q", fpOne, fpDup)
	}
}

func TestRetrieverConfigFingerprint_agenticEmptyReturnsEmpty(t *testing.T) {
	if fp := retrieverConfigFingerprint(RetrieverModeAgentic, nil); fp != "" {
		t.Fatalf("agentic with no retrievers should return empty string, got %q", fp)
	}
	// Explicit agentic with empty-name-only retrievers (all filtered out) also returns "".
	r := &retrieverExecuteStub{name: "  "}
	if fp := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{r}); fp != "" {
		t.Fatalf("agentic with blank-name-only retrievers should return empty string, got %q", fp)
	}
}

func TestRetrieverConfigFingerprint_agenticWithNamesNonEmpty(t *testing.T) {
	r := &retrieverExecuteStub{name: "kb"}
	fp := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{r})
	if fp == "" {
		t.Fatal("agentic with a named retriever should produce a non-empty fingerprint")
	}
	if len(fp) != 64 {
		t.Fatalf("expected 64-char hex digest, got %q (len=%d)", fp, len(fp))
	}
}

func TestRetrieverConfigFingerprint_modeAndNames(t *testing.T) {
	fpAgentic := retrieverConfigFingerprint(RetrieverModeAgentic, nil)
	fpPrefetch := retrieverConfigFingerprint(RetrieverModePrefetch, nil)
	if fpAgentic == fpPrefetch {
		t.Fatal("expected different fingerprints for agentic vs prefetch")
	}
	if fpPrefetch == "" {
		t.Fatal("prefetch mode should produce non-empty fingerprint even with no retrievers")
	}

	r1 := &retrieverExecuteStub{name: "wiki"}
	r2 := &retrieverExecuteStub{name: "docs"}
	fpOne := retrieverConfigFingerprint(RetrieverModePrefetch, []interfaces.Retriever{r1})
	fpTwo := retrieverConfigFingerprint(RetrieverModePrefetch, []interfaces.Retriever{r1, r2})
	if fpOne == fpTwo {
		t.Fatal("expected different fingerprints when retriever names differ")
	}
}

func TestRetrieverConfigFingerprint_hybridDiffersFromAgenticAndPrefetch(t *testing.T) {
	fpHybrid := retrieverConfigFingerprint(RetrieverModeHybrid, nil)
	if fpHybrid == "" {
		t.Fatal("hybrid mode should produce a non-empty fingerprint even with no retrievers")
	}
	fpAgentic := retrieverConfigFingerprint(RetrieverModeAgentic, nil)
	if fpHybrid == fpAgentic {
		t.Fatal("hybrid fingerprint must differ from agentic fingerprint")
	}
	fpPrefetch := retrieverConfigFingerprint(RetrieverModePrefetch, nil)
	if fpHybrid == fpPrefetch {
		t.Fatal("hybrid fingerprint must differ from prefetch fingerprint")
	}
}

func TestRetrieverConfigFingerprint_hybridNamesChangeDigest(t *testing.T) {
	r1 := &retrieverExecuteStub{name: "wiki"}
	r2 := &retrieverExecuteStub{name: "docs"}
	fpOne := retrieverConfigFingerprint(RetrieverModeHybrid, []interfaces.Retriever{r1})
	fpTwo := retrieverConfigFingerprint(RetrieverModeHybrid, []interfaces.Retriever{r1, r2})
	if fpOne == fpTwo {
		t.Fatal("expected different hybrid fingerprints when retriever names differ")
	}
}

func TestRetrieverConfigFingerprint_stability(t *testing.T) {
	r := &retrieverExecuteStub{name: "kb"}
	retrievers := []interfaces.Retriever{r}
	for _, mode := range []RetrieverMode{RetrieverModeAgentic, RetrieverModePrefetch, RetrieverModeHybrid} {
		fp1 := retrieverConfigFingerprint(mode, retrievers)
		fp2 := retrieverConfigFingerprint(mode, retrievers)
		if fp1 != fp2 {
			t.Fatalf("fingerprint not stable for mode %q: %q vs %q", mode, fp1, fp2)
		}
	}
}

func TestRetrieverConfigFingerprint_nameOrderDoesNotMatter(t *testing.T) {
	a := &retrieverExecuteStub{name: "alpha"}
	b := &retrieverExecuteStub{name: "beta"}
	fp1 := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{a, b})
	fp2 := retrieverConfigFingerprint(RetrieverModeAgentic, []interfaces.Retriever{b, a})
	if fp1 != fp2 {
		t.Fatalf("fingerprint must be order-independent: %q vs %q", fp1, fp2)
	}
}

func TestAgentConfigFingerprint_RetrieverNamesChangesDigest(t *testing.T) {
	baseOpts := []Option{
		WithName("test"),
		withTestTemporal("q"),
		WithLLMClient(testLLM(t)),
		WithRetrieverMode(RetrieverModePrefetch),
	}
	cfgNoR, err := buildAgentConfig(baseOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfgWithR, err := buildAgentConfig(append(baseOpts, WithRetrievers(&retrieverExecuteStub{name: "wiki"})))
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(cfgNoR) == agentConfigFingerprint(cfgWithR) {
		t.Fatal("expected different fingerprints when retriever names are registered")
	}
}
