package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseToolApproval(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameTool,
		Value: map[string]any{
			"agentName":       "a",
			"toolCallId":      "tc-1",
			"toolName":        "search",
			"toolDisplayName": "Search",
			"args":            map[string]any{"q": "hello"},
			"approvalToken":   "tok-tool",
		},
	}
	got, err := ParseToolApproval(req)
	require.NoError(t, err)
	require.Equal(t, "a", got.AgentName)
	require.Equal(t, "tc-1", got.ToolCallID)
	require.Equal(t, "search", got.ToolName)
	require.Equal(t, "Search", got.ToolDisplayName)
	require.Equal(t, "tok-tool", got.ApprovalToken)
	require.Equal(t, "hello", got.Args["q"])
}

func TestParseToolApproval_TypedValue(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameTool,
		Value: ToolApprovalRequestValue{
			ToolName:      "calc",
			ToolCallID:    "c1",
			ApprovalToken: "tok",
			Args:          map[string]any{"x": 1},
		},
	}
	got, err := ParseToolApproval(req)
	require.NoError(t, err)
	require.Equal(t, "calc", got.ToolName)
	require.Equal(t, "c1", got.ToolCallID)
}

func TestParseToolApproval_PointerValue(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameTool,
		Value: &ToolApprovalRequestValue{
			ToolName:      "ptr",
			ApprovalToken: "tok-p",
		},
	}
	got, err := ParseToolApproval(req)
	require.NoError(t, err)
	require.Equal(t, "ptr", got.ToolName)
}

func TestParseToolApproval_Errors(t *testing.T) {
	_, err := ParseToolApproval(nil)
	require.Error(t, err)

	_, err = ParseToolApproval(&ApprovalRequest{Name: ApprovalRequestNameBudget, Value: map[string]any{}})
	require.Error(t, err)

	_, err = ParseToolApproval(&ApprovalRequest{Name: ApprovalRequestNameTool})
	require.Error(t, err)

	_, err = ParseToolApproval(&ApprovalRequest{
		Name:  ApprovalRequestNameTool,
		Value: (*ToolApprovalRequestValue)(nil),
	})
	require.Error(t, err)
}

func TestParseDelegationApproval(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameSubAgent,
		Value: map[string]any{
			"agentName":     "parent",
			"subAgentName":  "child",
			"args":          map[string]any{"task": "summarize"},
			"approvalToken": "tok-del",
		},
	}
	got, err := ParseDelegationApproval(req)
	require.NoError(t, err)
	require.Equal(t, "parent", got.AgentName)
	require.Equal(t, "child", got.SubAgentName)
	require.Equal(t, "tok-del", got.ApprovalToken)
	require.Equal(t, "summarize", got.Args["task"])
}

func TestParseDelegationApproval_TypedValue(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameSubAgent,
		Value: SubAgentDelegationApprovalRequestValue{
			SubAgentName:  "researcher",
			ApprovalToken: "tok",
		},
	}
	got, err := ParseDelegationApproval(req)
	require.NoError(t, err)
	require.Equal(t, "researcher", got.SubAgentName)
}

func TestParseDelegationApproval_Errors(t *testing.T) {
	_, err := ParseDelegationApproval(nil)
	require.Error(t, err)

	_, err = ParseDelegationApproval(&ApprovalRequest{Name: ApprovalRequestNameTool, Value: map[string]any{}})
	require.Error(t, err)

	_, err = ParseDelegationApproval(&ApprovalRequest{Name: ApprovalRequestNameSubAgent})
	require.Error(t, err)
}

func TestParseBudgetApproval(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameBudget,
		Value: map[string]any{
			"agentName":     "a",
			"detail":        "over limit",
			"totalTokens":   float64(120),
			"costUsd":       0.01,
			"approvalToken": "tok-b",
		},
	}
	got, err := ParseBudgetApproval(req)
	require.NoError(t, err)
	require.Equal(t, "a", got.AgentName)
	require.Equal(t, "over limit", got.Detail)
	require.Equal(t, int64(120), got.TotalTokens)
	require.InDelta(t, 0.01, got.CostUSD, 1e-9)
	require.Equal(t, "tok-b", got.ApprovalToken)
}

func TestParseBudgetApproval_TypedValue(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameBudget,
		Value: BudgetApprovalRequestValue{
			AgentName:     "b",
			TotalTokens:   50,
			ApprovalToken: "tok",
		},
	}
	got, err := ParseBudgetApproval(req)
	require.NoError(t, err)
	require.Equal(t, "b", got.AgentName)
	require.Equal(t, int64(50), got.TotalTokens)
}

func TestParseBudgetApproval_PointerValue(t *testing.T) {
	req := &ApprovalRequest{
		Name: ApprovalRequestNameBudget,
		Value: &BudgetApprovalRequestValue{
			AgentName:     "ptr",
			TotalTokens:   10,
			ApprovalToken: "tok-p",
		},
	}
	got, err := ParseBudgetApproval(req)
	require.NoError(t, err)
	require.Equal(t, "ptr", got.AgentName)
	require.Equal(t, int64(10), got.TotalTokens)
}

func TestParseBudgetApproval_Errors(t *testing.T) {
	_, err := ParseBudgetApproval(nil)
	require.Error(t, err)

	_, err = ParseBudgetApproval(&ApprovalRequest{Name: ApprovalRequestNameTool, Value: map[string]any{}})
	require.Error(t, err)

	_, err = ParseBudgetApproval(&ApprovalRequest{Name: ApprovalRequestNameBudget})
	require.Error(t, err)
}
