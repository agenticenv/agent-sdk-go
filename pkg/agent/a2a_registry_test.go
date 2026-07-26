package agent

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestA2ARegistry_RegisterClient(t *testing.T) {
	r := NewA2ARegistry(nil)
	cl := testA2AClient(t, "agent1", nil)
	if err := r.RegisterClient(cl); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("agent1"); err != nil {
		t.Fatalf("Get(agent1) err = %v", err)
	}
}

func TestA2ARegistry_RegisterMissingURL(t *testing.T) {
	r := NewA2ARegistry(nil)
	if err := r.Register("remote", A2AConfig{}); err == nil {
		t.Fatal("expected error for missing URL")
	}
}

func TestA2ARegistry_UnregisterNotFound(t *testing.T) {
	r := NewA2ARegistry(nil)
	if err := r.Unregister("missing"); err != ErrRegistryNotFound {
		t.Errorf("err = %v, want ErrRegistryNotFound", err)
	}
}

func TestNormalizeA2ARegistry_fromWithA2AClients(t *testing.T) {
	cl := testA2AClient(t, "agent1", nil)
	c := &agentConfig{a2aClients: []interfaces.A2AClient{cl}}
	if err := c.buildA2ARegistry(); err != nil {
		t.Fatal(err)
	}
	if c.a2aRegistry == nil {
		t.Fatal("expected a2aRegistry after buildA2ARegistry")
	}
	got, err := c.a2aRegistry.Get("agent1")
	if err != nil || got != cl {
		t.Fatalf("Get(agent1) = %v, %v", got, err)
	}
}
