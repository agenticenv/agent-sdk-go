package tools

import (
	"context"
	"testing"

	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
)

// mockTool for registry tests (avoids import cycle with calculator/echo).
type mockTool struct {
	name string
}

func (m mockTool) Name() string                     { return m.name }
func (m mockTool) Description() string              { return "mock" }
func (m mockTool) Parameters() interfaces.JSONSchema { return interfaces.JSONSchema{} }
func (m mockTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	return nil, nil
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry should not return nil")
	}
	tools := r.Tools()
	if len(tools) != 0 {
		t.Errorf("new registry should have 0 tools, got %d", len(tools))
	}
}

func TestRegistry_RegisterGet(t *testing.T) {
	r := NewRegistry()
	mt := mockTool{name: "mock1"}
	r.Register(mt)

	tool, ok := r.Get("mock1")
	if !ok || tool == nil {
		t.Fatal("Get(mock1) should return tool")
	}
	if tool.Name() != "mock1" {
		t.Errorf("tool.Name() = %q, want mock1", tool.Name())
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) should return false")
	}
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	r := NewRegistry()
	mt := mockTool{name: "mock1"}
	r.Register(mt)
	r.Register(mt) // same tool again

	tools := r.Tools()
	if len(tools) != 1 {
		t.Errorf("overwrite should keep 1 tool, got %d", len(tools))
	}
}

func TestRegistry_RegisterNil(t *testing.T) {
	r := NewRegistry()
	r.Register(nil)

	tools := r.Tools()
	if len(tools) != 0 {
		t.Error("Register(nil) should be ignored")
	}
}

func TestRegistry_ToolsOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(mockTool{name: "a"})
	r.Register(mockTool{name: "b"})
	r.Register(mockTool{name: "a"}) // overwrite a

	tools := r.Tools()
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name() != "a" || tools[1].Name() != "b" {
		t.Errorf("Tools order = %q, %q; want a, b", tools[0].Name(), tools[1].Name())
	}
}
