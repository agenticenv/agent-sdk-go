package restate

import (
	"errors"
	"fmt"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

// ErrNotApprovalCustomEvent means the CUSTOM event name is not tool, delegation, or budget approval.
var ErrNotApprovalCustomEvent = errors.New("restate: custom event is not a recognized approval kind")

func toolApprovalFromEventValue(ev events.AgentCustomEventApprovalValue) types.ToolApprovalRequestValue {
	return types.ToolApprovalRequestValue{
		AgentName:       ev.AgentName,
		ToolCallID:      ev.ToolCallID,
		ToolName:        ev.ToolName,
		ToolDisplayName: ev.ToolDisplayName,
		Args:            cloneArgsMap(ev.Args),
		ApprovalToken:   ev.ApprovalToken,
	}
}

func delegationApprovalFromEventValue(ev events.AgentCustomEventDelegationValue) types.SubAgentDelegationApprovalRequestValue {
	return types.SubAgentDelegationApprovalRequestValue{
		AgentName:     ev.AgentName,
		SubAgentName:  ev.SubAgentName,
		Args:          cloneArgsMap(ev.Args),
		ApprovalToken: ev.ApprovalToken,
	}
}

func budgetApprovalFromEventValue(ev events.AgentCustomEventBudgetValue) types.BudgetApprovalRequestValue {
	return types.BudgetApprovalRequestValue{
		AgentName:     ev.AgentName,
		Detail:        ev.Detail,
		TotalTokens:   ev.TotalTokens,
		CostUSD:       ev.CostUSD,
		ApprovalToken: ev.ApprovalToken,
	}
}

// prepareApprovalFromCustomEvent parses a CUSTOM event into an SDK approval request + token.
func prepareApprovalFromCustomEvent(ev *events.AgentCustomEvent) (req *types.ApprovalRequest, approvalToken string, err error) {
	if ev == nil {
		return nil, "", fmt.Errorf("restate: nil custom event")
	}
	switch events.AgentCustomEventName(ev.Name) {
	case events.AgentCustomEventNameToolApproval:
		raw, err := events.ParseCustomEventApproval(ev)
		if err != nil {
			return nil, "", err
		}
		v := toolApprovalFromEventValue(raw)
		return &types.ApprovalRequest{Name: types.ApprovalRequestNameTool, Value: v}, v.ApprovalToken, nil
	case events.AgentCustomEventNameSubAgentDelegation:
		raw, err := events.ParseCustomEventDelegation(ev)
		if err != nil {
			return nil, "", err
		}
		v := delegationApprovalFromEventValue(raw)
		return &types.ApprovalRequest{Name: types.ApprovalRequestNameSubAgent, Value: v}, v.ApprovalToken, nil
	case events.AgentCustomEventNameBudget:
		raw, err := events.ParseCustomEventBudget(ev)
		if err != nil {
			return nil, "", err
		}
		v := budgetApprovalFromEventValue(raw)
		return &types.ApprovalRequest{Name: types.ApprovalRequestNameBudget, Value: v}, v.ApprovalToken, nil
	default:
		return nil, "", ErrNotApprovalCustomEvent
	}
}

func cloneArgsMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
