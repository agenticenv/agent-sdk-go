package temporal_test

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

func TestWithTemporalConfig_EmptyTaskQueue(t *testing.T) {
	_, err := agent.NewAgent(
		agent.WithName("t"),
		agent.WithLLMClient(stubLLM{}),
		agenttemporal.WithTemporalConfig(&agenttemporal.TemporalConfig{TaskQueue: ""}),
	)
	if err == nil || !strings.Contains(err.Error(), "TaskQueue") {
		t.Fatalf("got %v", err)
	}
}

func TestTemporalAndRestate_Conflict(t *testing.T) {
	_, err := agent.NewAgent(
		agent.WithName("t"),
		agent.WithLLMClient(stubLLM{}),
		agenttemporal.WithTemporalConfig(&agenttemporal.TemporalConfig{TaskQueue: "q"}),
		agentrestate.WithRestateConfig(&agentrestate.RestateConfig{}),
	)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("got %v", err)
	}
}
