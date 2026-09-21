package temporal

import (
	"context"
	"strings"
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

type fpTool struct{ name string }

func (f fpTool) Name() string                      { return f.name }
func (f fpTool) DisplayName() string               { return "FP Tool" }
func (f fpTool) Description() string               { return "" }
func (f fpTool) Parameters() interfaces.JSONSchema { return nil }
func (f fpTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	return nil, nil
}

func TestComputeAgentFingerprint_toolOrderStable(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	hA := ComputeAgentFingerprint(BuildAgentFingerprintPayload(spec, []string{"a", "b", "c"}, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", ""))
	hB := ComputeAgentFingerprint(BuildAgentFingerprintPayload(spec, []string{"c", "a", "b"}, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", ""))
	if hA != hB {
		t.Fatalf("tool order should not matter: %q vs %q", hA, hB)
	}
}

func TestComputeAgentFingerprint_stableWithoutTools(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	interactive := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	autonomous := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "autonomous", "", "", "", "", "")
	if ComputeAgentFingerprint(interactive) == ComputeAgentFingerprint(autonomous) {
		t.Fatal("expected different digests for autonomous vs interactive")
	}
}

func TestComputeAgentFingerprint_mcpFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	tools := []string{"mcp_srv_echo"}
	base := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	withMCP := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "abc123deadbeef", "", "", "", "", "", "", "", "")
	h0 := ComputeAgentFingerprint(base)
	h1 := ComputeAgentFingerprint(withMCP)
	if h0 == h1 {
		t.Fatalf("expected different digests when mcp fingerprint set: %q vs %q", h0, h1)
	}
}

func TestComputeAgentFingerprint_a2aFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	tools := []string{"a2a_remote_echo"}
	base := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	withA2A := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "", "a2afp_deadbeef", "", "", "", "", "", "", "")
	h0 := ComputeAgentFingerprint(base)
	h1 := ComputeAgentFingerprint(withA2A)
	if h0 == h1 {
		t.Fatalf("expected different digests when a2a fingerprint set: %q vs %q", h0, h1)
	}
}

func TestComputeAgentFingerprint_retrieverFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	empty := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	withFP := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "retriever_fp_deadbeef", "", "", "")
	if ComputeAgentFingerprint(empty) == ComputeAgentFingerprint(withFP) {
		t.Fatal("expected different digests when retriever fingerprint set")
	}
}

func TestComputeAgentFingerprint_observabilityFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	tools := []string{"t1"}
	base := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	withObs := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "", "", "obs_deadbeef", "", "", "", "", "", "")
	h0 := ComputeAgentFingerprint(base)
	h1 := ComputeAgentFingerprint(withObs)
	if h0 == h1 {
		t.Fatalf("expected different digests when observability fingerprint set: %q vs %q", h0, h1)
	}
}

func TestComputeAgentFingerprint_errorControlFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	empty := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	withEC := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "ec_fp_deadbeef")
	if ComputeAgentFingerprint(empty) == ComputeAgentFingerprint(withEC) {
		t.Fatal("expected different digests when error control fingerprint set")
	}
}

func TestComputeAgentFingerprint_namedLLMFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	empty := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	withNamed := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "", "named_llm_fp_deadbeef", "")
	if ComputeAgentFingerprint(empty) == ComputeAgentFingerprint(withNamed) {
		t.Fatal("expected different digests when named LLM fingerprint set")
	}
}

func TestComputeAgentFingerprint_hooksFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	empty := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "", "", "")
	withHooks := BuildAgentFingerprintPayload(spec, nil, "auto", nil, 0, lim, "", "", "", "", "", "", "hooks_fp_deadbeef", "", "")
	if ComputeAgentFingerprint(empty) == ComputeAgentFingerprint(withHooks) {
		t.Fatal("expected different digests when hooks fingerprint set")
	}
}

func TestVerifyAgentFingerprint_mismatch(t *testing.T) {
	rt := &TemporalRuntime{
		resolveToolsFn: func(context.Context) ([]interfaces.Tool, error) {
			return nil, nil
		},
	}
	err := rt.verifyAgentFingerprint(context.Background(), "caller-fp", nil)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestVerifyAgentFingerprint_emptyCallerFingerprintSkipsCheck(t *testing.T) {
	rt := &TemporalRuntime{
		resolveToolsFn: func(context.Context) ([]interfaces.Tool, error) {
			return nil, nil
		},
	}
	if err := rt.verifyAgentFingerprint(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAgentFingerprint_disableCheckAllowsMismatch(t *testing.T) {
	rt := &TemporalRuntime{
		disableFingerprintCheck: true,
		resolveToolsFn: func(context.Context) ([]interfaces.Tool, error) {
			return nil, nil
		},
	}
	if err := rt.verifyAgentFingerprint(context.Background(), "caller-fp", nil); err != nil {
		t.Fatalf("expected bypass when skip is enabled, got: %v", err)
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
	p := BuildAgentFingerprintPayload(spec, []string{"t1"}, "p", sampling, 5, lim, "mcpfp", "", "", "", "", "", "", "", "")
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

func TestFetchTools_requiresResolver(t *testing.T) {
	rt := &TemporalRuntime{}
	_, err := rt.fetchTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tools resolver is not configured") {
		t.Fatalf("fetchTools() = %v, want resolver not configured error", err)
	}
}

func TestFetchTools_delegatesToResolver(t *testing.T) {
	want := []interfaces.Tool{fpTool{name: "t1"}}
	rt := &TemporalRuntime{
		resolveToolsFn: func(context.Context) ([]interfaces.Tool, error) {
			return want, nil
		},
	}
	got, err := rt.fetchTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name() != "t1" {
		t.Fatalf("fetchTools() = %+v, want t1", got)
	}
}

func TestVerifyAgentFingerprint_usesPreFetchedTools(t *testing.T) {
	tools := []interfaces.Tool{fpTool{name: "a"}}
	rt := &TemporalRuntime{
		Runtime: base.Runtime{
			AgentSpec: sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"},
			AgentConfig: sdkruntime.AgentConfig{
				Limits: sdkruntime.AgentLimits{MaxIterations: 3},
			},
		},
		resolveToolsFn: func(context.Context) ([]interfaces.Tool, error) {
			t.Fatal("fetchTools should not run when tools are pre-fetched")
			return nil, nil
		},
	}
	fp := computeAgentFingerprintFromRuntime(rt, tools)
	if err := rt.verifyAgentFingerprint(context.Background(), fp, tools); err != nil {
		t.Fatal(err)
	}
}
