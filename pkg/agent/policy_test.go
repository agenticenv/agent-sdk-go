package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// mockTool implements interfaces.Tool for policy tests.
type mockTool struct {
	name string
}

func (m mockTool) Name() string                      { return m.name }
func (m mockTool) Description() string               { return "mock" }
func (m mockTool) Parameters() interfaces.JSONSchema { return interfaces.JSONSchema{} }
func (m mockTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	return nil, nil
}

func TestToolPolicyFingerprint(t *testing.T) {
	if toolPolicyFingerprint(nil) != "nil" {
		t.Fatal("nil policy")
	}
	if toolPolicyFingerprint(RequireAllToolApprovalPolicy{}) != "require_all" {
		t.Fatal("require_all value")
	}
	p := AutoToolApprovalPolicy()
	if toolPolicyFingerprint(p) != "auto" {
		t.Fatal("auto")
	}
	al, err := AllowlistToolApprovalPolicy(AllowlistToolApprovalConfig{ToolNames: []string{"z", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	fp := toolPolicyFingerprint(al)
	if !strings.HasPrefix(fp, "allowlist:") || !strings.Contains(fp, "a") || !strings.Contains(fp, "z") {
		t.Fatalf("got %q", fp)
	}
	if got := toolPolicyFingerprint(unknownPolicyForFingerprintTest{}); !strings.Contains(got, "unknown") {
		t.Fatalf("got %q", got)
	}
}

type unknownPolicyForFingerprintTest struct{}

func (unknownPolicyForFingerprintTest) RequiresApproval(interfaces.Tool) bool { return false }

func TestRequireAllToolApprovalPolicy_RequiresApproval(t *testing.T) {
	p := RequireAllToolApprovalPolicy{}
	tool := mockTool{name: "any"}
	if !p.RequiresApproval(tool) {
		t.Error("RequireAllToolApprovalPolicy should require approval for all tools")
	}
}

func TestAutoToolApprovalPolicy_RequiresApproval(t *testing.T) {
	p := AutoToolApprovalPolicy()
	tool := mockTool{name: "any"}
	if p.RequiresApproval(tool) {
		t.Error("AutoToolApprovalPolicy should not require approval for any tool")
	}
}

func TestAllowlistToolApprovalPolicy(t *testing.T) {
	allowed, err := AllowlistToolApprovalPolicy(AllowlistToolApprovalConfig{ToolNames: []string{"calculator", "echo"}})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		toolName       string
		expectApproval bool
	}{
		{"calculator", false},
		{"echo", false},
		{"search", true},
		{"weather", true},
	}

	for _, tt := range tests {
		tool := mockTool{name: tt.toolName}
		got := allowed.RequiresApproval(tool)
		if got != tt.expectApproval {
			t.Errorf("tool %q: RequiresApproval = %v, want %v", tt.toolName, got, tt.expectApproval)
		}
	}
}

func TestAllowlistToolApprovalPolicy_invalidSubAgentName(t *testing.T) {
	_, err := AllowlistToolApprovalPolicy(AllowlistToolApprovalConfig{
		SubAgentNames: []string{"@@@"},
	})
	if err == nil {
		t.Fatal("expected error for sub-agent name with no alphanumeric characters")
	}
}

func TestAllowlistToolApprovalPolicy_configured(t *testing.T) {
	mathAgent := &Agent{agentConfig: agentConfig{Name: "Math Specialist", ID: "id-math"}}
	wantSub, err := subAgentToolName(mathAgent.Name)
	if err != nil {
		t.Fatal(err)
	}

	p, err := AllowlistToolApprovalPolicy(AllowlistToolApprovalConfig{
		ToolNames:     []string{"echo", "", "custom_exact"},
		SubAgentNames: []string{"Math Specialist"},
		MCPTools: map[string][]string{
			"remote": {"search", ""},
			"":       {"skip"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		toolName       string
		expectApproval bool
	}{
		{"echo", false},
		{"calculator", true},
		{wantSub, false},
		{"mcp_remote_search", false},
		{"mcp_other_search", true},
		{"custom_exact", false},
	}

	for _, tt := range tests {
		tool := mockTool{name: tt.toolName}
		got := p.RequiresApproval(tool)
		if got != tt.expectApproval {
			t.Errorf("tool %q: RequiresApproval = %v, want %v", tt.toolName, got, tt.expectApproval)
		}
	}
}
