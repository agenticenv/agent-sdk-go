package restate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentrestate "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate"
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

func TestWithRestateConfig_NilConfig(t *testing.T) {
	_, err := agent.NewAgent(
		agent.WithName("r"),
		agent.WithLLMClient(stubLLM{}),
		agentrestate.WithRestateConfig(nil),
	)
	if err == nil || !strings.Contains(err.Error(), "WithRestateConfig") {
		t.Fatalf("got %v", err)
	}
}

func TestRestateAndTemporal_Conflict(t *testing.T) {
	_, err := agent.NewAgent(
		agent.WithName("r"),
		agent.WithLLMClient(stubLLM{}),
		agentrestate.WithRestateConfig(&agentrestate.RestateConfig{}),
		agenttemporal.WithTemporalConfig(&agenttemporal.TemporalConfig{TaskQueue: "q"}),
	)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("got %v", err)
	}
}

func TestNewAgentWorker_RejectsRestate(t *testing.T) {
	_, err := agent.NewAgentWorker(
		agent.WithName("r"),
		agent.WithLLMClient(stubLLM{}),
		agentrestate.WithRestateConfig(&agentrestate.RestateConfig{}),
	)
	if err == nil || !strings.Contains(err.Error(), "Temporal") {
		t.Fatalf("got %v", err)
	}
}
