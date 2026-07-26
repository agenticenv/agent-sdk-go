package agent

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestMCPRegistry_RegisterClient(t *testing.T) {
	r := NewMCPRegistry(nil)
	cl := testMCPClient(t, "srv")
	if err := r.RegisterClient(cl); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get("srv")
	if err != nil || got != cl {
		t.Fatalf("Get(srv) = %v, %v", got, err)
	}
	if len(r.List()) != 1 {
		t.Fatalf("List len = %d, want 1", len(r.List()))
	}
	if err := r.Unregister("srv"); err != nil {
		t.Fatal(err)
	}
}

func TestMCPRegistry_RegisterConfigMissingTransport(t *testing.T) {
	r := NewMCPRegistry(nil)
	err := r.Register("bad", MCPConfig{})
	if err == nil {
		t.Fatal("expected error for missing transport")
	}
}

func TestMCPRegistry_RegisterDuplicate(t *testing.T) {
	r := NewMCPRegistry(nil)
	cl := testMCPClient(t, "srv")
	if err := r.RegisterClient(cl); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterClient(testMCPClient(t, "srv")); err != ErrRegistryDuplicate {
		t.Errorf("duplicate RegisterClient err = %v, want ErrRegistryDuplicate", err)
	}
}

func TestNormalizeMCPRegistry_fromWithMCPClients(t *testing.T) {
	cl := testMCPClient(t, "srv")
	c := &agentConfig{mcpClients: []interfaces.MCPClient{cl}}
	if err := c.buildMCPRegistry(); err != nil {
		t.Fatal(err)
	}
	if c.mcpRegistry == nil {
		t.Fatal("expected mcpRegistry after buildMCPRegistry")
	}
	got, err := c.mcpRegistry.Get("srv")
	if err != nil || got != cl {
		t.Fatalf("Get(srv) = %v, %v", got, err)
	}
}
