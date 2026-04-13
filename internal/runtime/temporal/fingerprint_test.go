package temporal

import (
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
)

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
	)
	h1 := ComputeAgentFingerprint(m)
	h2 := ComputeAgentFingerprint(m)
	if len(h1) != 64 || h1 != h2 {
		t.Fatalf("fingerprint len=%d h1=%q h2=%q", len(h1), h1, h2)
	}

	hA := ComputeAgentFingerprint(BuildAgentFingerprintPayload(spec, []string{"a", "b", "c"}, "auto", nil, 0, lim, ""))
	hB := ComputeAgentFingerprint(BuildAgentFingerprintPayload(spec, []string{"c", "a", "b"}, "auto", nil, 0, lim, ""))
	if hA != hB {
		t.Fatalf("tool order should not matter: %q vs %q", hA, hB)
	}
}

func TestComputeAgentFingerprint_mcpFingerprintChangesDigest(t *testing.T) {
	spec := sdkruntime.AgentSpec{Name: "a", SystemPrompt: "p"}
	lim := sdkruntime.AgentLimits{MaxIterations: 3}
	tools := []string{"mcp_srv_echo"}
	base := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "")
	withMCP := BuildAgentFingerprintPayload(spec, tools, "auto", nil, 0, lim, "abc123deadbeef")
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
