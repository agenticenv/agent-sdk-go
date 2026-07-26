package agent

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestNewToolRegistry(t *testing.T) {
	r := NewToolRegistry()
	if r == nil {
		t.Fatal("NewToolRegistry should not return nil")
	}
	if len(r.List()) != 0 {
		t.Errorf("new registry should have 0 tools, got %d", len(r.List()))
	}
}

func TestToolRegistry_RegisterGet(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(testTool(t, "mock1")); err != nil {
		t.Fatal(err)
	}

	tool, err := r.Get("mock1")
	if err != nil || tool == nil {
		t.Fatalf("Get(mock1) = %v, %v", tool, err)
	}
	if tool.Name() != "mock1" {
		t.Errorf("tool.Name() = %q, want mock1", tool.Name())
	}

	if _, err := r.Get("nonexistent"); err != ErrRegistryNotFound {
		t.Errorf("Get(nonexistent) err = %v, want ErrRegistryNotFound", err)
	}
}

func TestToolRegistry_RegisterDuplicate(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(testTool(t, "mock1")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testTool(t, "mock1")); err != ErrRegistryDuplicate {
		t.Errorf("second Register err = %v, want ErrRegistryDuplicate", err)
	}
}

func TestToolRegistry_RegisterNil(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(nil); err != ErrRegistryNilEntry {
		t.Errorf("Register(nil) err = %v, want ErrRegistryNilEntry", err)
	}
}

func TestToolRegistry_Unregister(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(testTool(t, "a")); err != nil {
		t.Fatal(err)
	}
	if err := r.Unregister("a"); err != nil {
		t.Fatalf("Unregister(a) err = %v", err)
	}
	if _, err := r.Get("a"); err != ErrRegistryNotFound {
		t.Error("Get(a) after Unregister should be ErrRegistryNotFound")
	}
	if err := r.Unregister("missing"); err != ErrRegistryNotFound {
		t.Errorf("Unregister(missing) err = %v, want ErrRegistryNotFound", err)
	}
}

func TestToolRegistry_ListOrder(t *testing.T) {
	r := NewToolRegistry()
	_ = r.Register(testTool(t, "a"))
	_ = r.Register(testTool(t, "b"))

	tools := r.List()
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name() != "a" || tools[1].Name() != "b" {
		t.Errorf("List order = %q, %q; want a, b", tools[0].Name(), tools[1].Name())
	}
}

func TestRegisterTools(t *testing.T) {
	r := NewToolRegistry()
	if err := RegisterTools(r, testTool(t, "a"), testTool(t, "b")); err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 2 {
		t.Fatalf("List len = %d, want 2", len(r.List()))
	}
}

func TestNormalizeToolRegistry_fromWithTools(t *testing.T) {
	c := &agentConfig{tools: []interfaces.Tool{testTool(t, "a")}}
	if err := c.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	if len(c.tools) != 0 {
		t.Fatal("WithTools entries should be cleared after buildToolRegistry")
	}
	if len(c.toolRegistry.List()) != 1 {
		t.Fatalf("registry len = %d, want 1", len(c.toolRegistry.List()))
	}
}

func TestNormalizeToolRegistry_userRegistryWins(t *testing.T) {
	reg := NewToolRegistry()
	_ = reg.Register(testTool(t, "existing"))
	c := &agentConfig{
		toolRegistry: reg,
		tools:        []interfaces.Tool{testTool(t, "from_with_tools")},
	}
	if err := c.buildToolRegistry(); err != nil {
		t.Fatal(err)
	}
	tools := c.toolRegistry.List()
	if len(tools) != 2 {
		t.Fatalf("registry len = %d, want 2", len(tools))
	}
}
