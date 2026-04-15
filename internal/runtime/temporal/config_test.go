package temporal

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	temporalmocks "go.temporal.io/sdk/mocks"
)

type stubLLM struct{}

func (stubLLM) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{}, nil
}
func (stubLLM) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stub")
}
func (stubLLM) GetModel() string                    { return "stub" }
func (stubLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (stubLLM) IsStreamSupported() bool             { return false }

func TestBuildTemporalRuntimeConfig_RequiresTemporalOrClient(t *testing.T) {
	// Neither WithTemporalConfig nor WithTemporalClient: must fail fast without dialing a server.
	options := []Option{
		WithLogger(logger.NoopLogger()),
		WithInstanceId("test"),
		WithEnableRemoteWorkers(false),
		WithRemoteWorker(false),
		WithPolicyFingerprint("test"),
		WithMCPFingerprint("test"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test"}),
		WithAgentExecution(sdkruntime.AgentExecution{
			LLM: sdkruntime.AgentLLM{Client: stubLLM{}},
		}),
	}
	_, err := buildTemporalRuntimeConfig(options...)
	if err == nil || !strings.Contains(err.Error(), "temporal config or client is required") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildTemporalRuntimeConfig_RequiresLLMClient(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	_, err := buildTemporalRuntimeConfig(
		WithTemporalClient(tc, "tq"),
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "x"}),
		WithAgentExecution(sdkruntime.AgentExecution{}),
	)
	if err == nil || !strings.Contains(err.Error(), "llm client is required") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildTemporalRuntimeConfig_InstanceIdSuffix(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	cfg, err := buildTemporalRuntimeConfig(
		WithTemporalClient(tc, "myq"),
		WithInstanceId("pod1"),
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "x"}),
		WithAgentExecution(sdkruntime.AgentExecution{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.taskQueue != "myq-pod1" {
		t.Fatalf("taskQueue = %q, want myq-pod1", cfg.taskQueue)
	}
}
