package temporal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
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
	_, err := buildTemporalRuntimeConfig(WithLLMClient(stubLLM{}))
	if err == nil || !strings.Contains(err.Error(), "temporal config or client is required") {
		t.Fatalf("got %v", err)
	}
}
