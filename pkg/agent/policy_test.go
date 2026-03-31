package agent

import (
	"context"
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
	allowed := AllowlistToolApprovalPolicy("calculator", "echo")

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
