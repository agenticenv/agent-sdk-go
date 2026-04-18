package temporal

import (
	"context"
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

type fpTool struct{ name string }

func (f fpTool) Name() string                      { return f.name }
func (f fpTool) Description() string               { return "" }
func (f fpTool) Parameters() interfaces.JSONSchema { return nil }
func (f fpTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	return nil, nil
}

func TestComputeAgentFingerprint_stableAndToolOrder(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}

	m := BuildAgentFingerprintPayload(
		spec,
		[]string{"z", "a"},
		"auto",
		nil,
		10,
		lim,
		"",
		"",
	)
	h1 := ComputeAgentFingerprint(m)
	h2 := ComputeAgentFingerprint(m)
	if len(h1) != 64 || h1 != h2 {
		t.Fatalf("fingerprint len=%d h1=%q h2=%q", len(h1), h1, h2)
	}

	hA := ComputeAgentFingerprint(BuildAgentFingerprintPayload(spec, []string{"a", "b", "c"}, "auto", nil, 0, lim, "", ""))
	hB := ComputeAgentFingerprint(BuildAgentFingerprintPayload(spec, []string{"c", "a", "b"}, "auto", nil, 0, lim, "", ""))
	if hA != hB {
		t.Fatalf("tool order should not matter: %q vs %q", hA, hB)
	}
}

func TestComputeAgentFingerprint_agentModeChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	interactive := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "")
	autonomous := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "autonomous")
	if ComputeAgentFingerprint(interactive) == ComputeAgentFingerprint(autonomous) {
		t.Fatal("expected different digests for autonomous vs interactive")
	}
}

func TestComputeAgentFingerprint_mcpFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	tools := []string{"mcp_srv_echo"}
	base := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "", "")
	withMCP := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "abc123deadbeef", "")
	h0 := ComputeAgentFingerprint(base)
	h1 := ComputeAgentFingerprint(withMCP)
	if h0 == h1 {
		t.Fatalf("expected different digests when mcp fingerprint set: %q vs %q", h0, h1)
	}
}

func TestVerifyAgentFingerprint_mismatch(t *testing.T) {
	cfg := &TemporalRuntimeConfig{
		AgentSpec: sdkruntime.AgentSpec{Name: "x"},
		AgentExecution: sdkruntime.AgentExecution{
			LLM:     sdkruntime.AgentLLM{},
			Tools:   sdkruntime.AgentTools{Tools: nil},
			Session: sdkruntime.AgentSession{},
			Limits:  sdkruntime.AgentLimits{},
		},
		PolicyFingerprint: "require_all",
	}
	rt := &TemporalRuntime{
		TemporalRuntimeConfig: *cfg,
		agentFingerprint:      computeAgentFingerprintFromRuntimeConfig(cfg),
	}
	err := rt.verifyAgentFingerprint("deadbeef")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestVerifyAgentFingerprint_bothEmptyOK(t *testing.T) {
	rt := &TemporalRuntime{}
	if err := rt.verifyAgentFingerprint(""); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAgentFingerprint_emptyWantWhenWorkerHasFingerprint(t *testing.T) {
	cfg := &TemporalRuntimeConfig{
		AgentSpec: sdkruntime.AgentSpec{Name: "x"},
		AgentExecution: sdkruntime.AgentExecution{
			LLM:     sdkruntime.AgentLLM{},
			Tools:   sdkruntime.AgentTools{Tools: nil},
			Session: sdkruntime.AgentSession{},
			Limits:  sdkruntime.AgentLimits{},
		},
		PolicyFingerprint: "require_all",
	}
	rt := &TemporalRuntime{
		TemporalRuntimeConfig: *cfg,
		agentFingerprint:      computeAgentFingerprintFromRuntimeConfig(cfg),
	}
	if err := rt.verifyAgentFingerprint(""); err == nil {
		t.Fatal("expected mismatch when caller fingerprint is empty but worker has one")
	}
}

func TestToolNamesFromTools_skipsNil(t *testing.T) {
	if got := ToolNamesFromTools(nil); got != nil {
		t.Fatalf("nil tools: %v", got)
	}
	got := ToolNamesFromTools([]interfaces.Tool{nil, fpTool{name: "b"}, fpTool{name: "a"}})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
}

func TestBuildAgentFingerprintPayload_responseFormatAndSampling(t *testing.T) {
	spec := sdkruntime.AgentSpec{
		Name: "agent",
		ResponseFormat: &interfaces.ResponseFormat{
			Type:   interfaces.ResponseFormatJSON,
			Name:   "Out",
			Schema: interfaces.JSONSchema{"type": "object"},
		},
	}
	temp := 0.2
	sampling := &sdkruntime.LLMSampling{
		Temperature: &temp,
		Reasoning:   &interfaces.LLMReasoning{Effort: "low"},
	}
	lim := sdkruntime.AgentLimits{MaxIterations: 1, Timeout: 0, ApprovalTimeout: 0}
	p := BuildAgentFingerprintPayload(spec, []string{"t1"}, "p", sampling, 5, lim, "mcpfp", "")
	if p.ResponseFormat == nil || p.ResponseFormat.Type != string(interfaces.ResponseFormatJSON) {
		t.Fatalf("response format: %+v", p.ResponseFormat)
	}
	if p.ResponseFormat.Schema == nil {
		t.Fatal("schema")
	}
	if p.Sampling == nil || p.Sampling.Temperature == nil || *p.Sampling.Temperature != 0.2 {
		t.Fatalf("sampling clone: %+v", p.Sampling)
	}
	if p.Sampling.Reasoning == nil || p.Sampling.Reasoning.Effort != "low" {
		t.Fatalf("reasoning clone: %+v", p.Sampling.Reasoning)
	}
	// Mutate original sampling; payload must not alias (cloneLLMSampling).
	nine := 0.9
	sampling.Temperature = &nine
	if p.Sampling.Temperature == nil || *p.Sampling.Temperature != 0.2 {
		t.Fatalf("payload temperature should stay 0.2 after original changes: %+v", p.Sampling.Temperature)
	}
}
