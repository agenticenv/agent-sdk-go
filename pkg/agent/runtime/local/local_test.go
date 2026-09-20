package local_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentlocal "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/local"
	agenttemporal "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

type stubLLM struct{}

func (stubLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{Content: "ok"}, nil
}
func (stubLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, nil
}
func (stubLLM) GetModel() string                    { return "stub" }
func (stubLLM) GetProvider() interfaces.LLMProvider { return "stub" }
func (stubLLM) IsStreamSupported() bool             { return false }

// TestWithLocalConfig_DurableByDefault confirms the local runtime journals to the
// configured DataDir when no Durability override is given (durable-by-default).
func TestWithLocalConfig_DurableByDefault(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	a, err := agent.NewAgent(
		agent.WithName("local-durable"),
		agent.WithLLMClient(stubLLM{}),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{DataDir: dataDir}),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	defer a.Close()

	run, err := a.Run(context.Background(), "hi", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := run.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("expected durable-go to create DataDir %s, got: %v", dataDir, err)
	}
}

func TestWithLocalConfig_AutoPurgeCaps(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	a, err := agent.NewAgent(
		agent.WithName("local-purge-caps"),
		agent.WithLLMClient(stubLLM{}),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{
			DataDir:           dataDir,
			AutoPurgeMaxRuns:  10,
			AutoPurgeMaxBytes: 1 << 20,
		}),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	defer a.Close()

	run, err := a.Run(context.Background(), "hi", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := run.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

// TestWithLocalConfig_DurabilityOff confirms DurabilityOff() restores the pre-durability
// in-memory path: no journal directory is created.
func TestWithLocalConfig_DurabilityOff(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	a, err := agent.NewAgent(
		agent.WithName("local-nondurable"),
		agent.WithLLMClient(stubLLM{}),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{
			DataDir:    dataDir,
			Durability: agentlocal.DurabilityOff(),
		}),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	defer a.Close()

	run, err := a.Run(context.Background(), "hi", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := run.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("expected no DataDir to be created when durability is off, stat err: %v", err)
	}
}

func TestWithLocalConfig_ConflictsWithTemporal(t *testing.T) {
	off := false
	_, err := agent.NewAgent(
		agent.WithName("l"),
		agent.WithLLMClient(stubLLM{}),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{Durability: &off}),
		agenttemporal.WithTemporalConfig(&agenttemporal.TemporalConfig{TaskQueue: "q"}),
	)
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("got %v", err)
	}
}

func TestWithLocalConfig_ConflictsWithTemporal_ReverseOrder(t *testing.T) {
	off := false
	_, err := agent.NewAgent(
		agent.WithName("l"),
		agent.WithLLMClient(stubLLM{}),
		agenttemporal.WithTemporalConfig(&agenttemporal.TemporalConfig{TaskQueue: "q"}),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{Durability: &off}),
	)
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("got %v", err)
	}
}

func TestWithLocalConfig_Nil(t *testing.T) {
	// A nil LocalConfig means "use defaults" (durable-by-default), not an error. Give an
	// explicit DataDir via a second call so this test doesn't touch ./agent_data.
	off := false
	a, err := agent.NewAgent(
		agent.WithName("local-nilcfg"),
		agent.WithLLMClient(stubLLM{}),
		agentlocal.WithLocalConfig(nil),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{DataDir: t.TempDir(), Durability: &off}),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	defer a.Close()
}

// TestWithLocalConfig_MultiReplicaLocking is a smoke test for the documented multi-replica
// DataDir guidance (see LocalConfig godoc): two engines pointed at the same DataDir cannot
// both be built; a distinct DataDir per replica must be used instead.
func TestWithLocalConfig_MultiReplicaLocking(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "shared")

	a1, err := agent.NewAgent(
		agent.WithName("replica-1"),
		agent.WithLLMClient(stubLLM{}),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{
			DataDir:     dataDir,
			LockTimeout: 50 * time.Millisecond,
		}),
	)
	if err != nil {
		t.Fatalf("NewAgent (replica 1): %v", err)
	}
	defer a1.Close()

	// Same DataDir, second in-process "replica": must fail fast rather than silently
	// sharing/corrupting the first engine's journal.
	_, err = agent.NewAgent(
		agent.WithName("replica-2"),
		agent.WithLLMClient(stubLLM{}),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{
			DataDir:     dataDir,
			LockTimeout: 50 * time.Millisecond,
		}),
	)
	if err == nil {
		t.Fatal("expected second engine on the same DataDir to fail to acquire the lock")
	}
}
