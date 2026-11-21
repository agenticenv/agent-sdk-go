package types

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
)

// maxApprovalTimeout caps how long a single approval wait may last in the run.
const MaxApprovalTimeout = 31 * 24 * time.Hour

type ApprovalStatus string

const (
	ApprovalStatusNone     ApprovalStatus = "NONE"
	ApprovalStatusPending  ApprovalStatus = "PENDING"
	ApprovalStatusApproved ApprovalStatus = "APPROVED"
	ApprovalStatusRejected ApprovalStatus = "REJECTED"
	// ApprovalStatusUnavailable means the approval request could not be delivered (e.g. event stream down). It is not a user rejection.
	ApprovalStatusUnavailable ApprovalStatus = "UNAVAILABLE"
	// ApprovalStatusTimedOut means the approval activity hit StartToCloseTimeout (ApprovalTimeout).
	// The tool is not executed; the agent loop continues.
	ApprovalStatusTimedOut ApprovalStatus = "TIMED_OUT"
)

// ApprovalSender sends an approval result. Call once per request. Safe for concurrent use—
// multiple approvals may be pending when tools run in parallel.
type ApprovalSender func(status ApprovalStatus) error

// ApprovalHandler is called when a tool needs approval (Run with WithApprovalHandler).
// req.Respond is always set; call req.Respond(Approved) or Rejected when ready.
type ApprovalHandler func(ctx context.Context, req *ApprovalRequest)

// ApprovalRequestName classifies the approval payload.
type ApprovalRequestName string

const (
	ApprovalRequestNameTool     ApprovalRequestName = "tool_approval"
	ApprovalRequestNameSubAgent ApprovalRequestName = "sub_agent_delegation"
	ApprovalRequestNameBudget   ApprovalRequestName = "budget_approval"
	ApprovalRequestNameDeferral ApprovalRequestName = "action_deferred"
)

// ApprovalRequest is one pending approval callback. Name + Value match CUSTOM semantics.
// Respond is always set. Resolve is set when an action policy issued the pause.
type ApprovalRequest struct {
	Name    ApprovalRequestName `json:"name,omitempty"`
	Value   any                 `json:"value,omitempty"`
	Respond ApprovalSender      `json:"-"`
	Resolve ActionResolver      `json:"-"`
}

// ActionResolution closes a policy pause. Method is human, automated, timeout, or unavailable.
// A deferred call approved with method human does not pause again for tool approval.
type ActionResolution struct {
	Status   ApprovalStatus `json:"status"`
	Resolver string         `json:"resolver,omitempty"`
	Method   string         `json:"method,omitempty"`
	Note     string         `json:"note,omitempty"`
}

// ActionResolver completes one policy pause. Call it once.
type ActionResolver func(ActionResolution) error

// ActionApprovalInfo is the policy context included on an approval or deferral request.
type ActionApprovalInfo struct {
	Decision     actionpolicy.Result        `json:"decision,omitempty"`
	Reason       string                     `json:"reason,omitempty"`
	Rule         string                     `json:"rule,omitempty"`
	Intent       string                     `json:"intent,omitempty"`
	Requester    actionpolicy.Requester     `json:"requester"`
	PriorActions []actionpolicy.PriorAction `json:"priorActions,omitempty"`
	Labels       []string                   `json:"labels,omitempty"`
}

// ToolApprovalRequestValue is the JSON payload for tool approval (same wire shape as CUSTOM approval value).
type ToolApprovalRequestValue struct {
	AgentName       string              `json:"agentName,omitempty"`
	ToolCallID      string              `json:"toolCallId,omitempty"`
	ToolName        string              `json:"toolName"`
	ToolDisplayName string              `json:"toolDisplayName,omitempty"`
	Args            map[string]any      `json:"args,omitempty"`
	ApprovalToken   string              `json:"approvalToken,omitempty"`
	Policy          *ActionApprovalInfo `json:"policy,omitempty"`
}

// SubAgentDelegationApprovalRequestValue is the JSON payload for sub-agent delegation approval.
type SubAgentDelegationApprovalRequestValue struct {
	AgentName     string              `json:"agentName,omitempty"`
	SubAgentName  string              `json:"subAgentName,omitempty"`
	Args          map[string]any      `json:"args,omitempty"`
	ApprovalToken string              `json:"approvalToken,omitempty"`
	Policy        *ActionApprovalInfo `json:"policy,omitempty"`
}

// ActionDeferredRequestValue is the payload for an action-policy deferral.
// It carries the same tool identity as a tool approval, plus the policy reason and context.
type ActionDeferredRequestValue struct {
	AgentName       string              `json:"agentName,omitempty"`
	ToolCallID      string              `json:"toolCallId,omitempty"`
	ToolName        string              `json:"toolName"`
	ToolDisplayName string              `json:"toolDisplayName,omitempty"`
	Args            map[string]any      `json:"args,omitempty"`
	ApprovalToken   string              `json:"approvalToken,omitempty"`
	Reason          string              `json:"reason,omitempty"`
	Rule            string              `json:"rule,omitempty"`
	Policy          *ActionApprovalInfo `json:"policy,omitempty"`
}

// BudgetApprovalRequestValue is the JSON payload for a per-run budget approval.
type BudgetApprovalRequestValue struct {
	AgentName     string  `json:"agentName,omitempty"`
	Detail        string  `json:"detail,omitempty"`
	TotalTokens   int64   `json:"totalTokens,omitempty"`
	CostUSD       float64 `json:"costUsd,omitempty"`
	ApprovalToken string  `json:"approvalToken,omitempty"`
}

func parseApprovalPayload[V any](v any) (out V, err error) {
	if v == nil {
		return out, fmt.Errorf("types: nil approval value")
	}
	switch x := v.(type) {
	case V:
		return x, nil
	case *V:
		if x == nil {
			return out, fmt.Errorf("types: nil approval value pointer")
		}
		return *x, nil
	default:
		raw, mErr := json.Marshal(v)
		if mErr != nil {
			return out, fmt.Errorf("types: marshal approval value: %w", mErr)
		}
		if uErr := json.Unmarshal(raw, &out); uErr != nil {
			return out, fmt.Errorf("types: unmarshal approval value: %w", uErr)
		}
		return out, nil
	}
}

// ParseToolApproval decodes Value for ApprovalRequestNameTool (handles map[string]any from JSON).
func ParseToolApproval(req *ApprovalRequest) (ToolApprovalRequestValue, error) {
	if req == nil {
		return ToolApprovalRequestValue{}, errors.New("types: nil approval request")
	}
	if req.Name != ApprovalRequestNameTool {
		return ToolApprovalRequestValue{}, errors.New("types: not a tool approval request")
	}
	if req.Value == nil {
		return ToolApprovalRequestValue{}, errors.New("types: tool approval request has empty value")
	}
	return parseApprovalPayload[ToolApprovalRequestValue](req.Value)
}

// ParseDelegationApproval decodes Value for ApprovalRequestNameSubAgent.
func ParseDelegationApproval(req *ApprovalRequest) (SubAgentDelegationApprovalRequestValue, error) {
	if req == nil {
		return SubAgentDelegationApprovalRequestValue{}, errors.New("types: nil approval request")
	}
	if req.Name != ApprovalRequestNameSubAgent {
		return SubAgentDelegationApprovalRequestValue{}, errors.New("types: not a sub-agent delegation approval request")
	}
	if req.Value == nil {
		return SubAgentDelegationApprovalRequestValue{}, errors.New("types: delegation approval request has empty value")
	}
	return parseApprovalPayload[SubAgentDelegationApprovalRequestValue](req.Value)
}

// ParseBudgetApproval decodes Value for ApprovalRequestNameBudget.
func ParseBudgetApproval(req *ApprovalRequest) (BudgetApprovalRequestValue, error) {
	if req == nil {
		return BudgetApprovalRequestValue{}, errors.New("types: nil approval request")
	}
	if req.Name != ApprovalRequestNameBudget {
		return BudgetApprovalRequestValue{}, errors.New("types: not a budget approval request")
	}
	if req.Value == nil {
		return BudgetApprovalRequestValue{}, errors.New("types: budget approval request has empty value")
	}
	return parseApprovalPayload[BudgetApprovalRequestValue](req.Value)
}

// ParseActionDeferred decodes Value for ApprovalRequestNameDeferral.
func ParseActionDeferred(req *ApprovalRequest) (ActionDeferredRequestValue, error) {
	if req == nil {
		return ActionDeferredRequestValue{}, errors.New("types: nil approval request")
	}
	if req.Name != ApprovalRequestNameDeferral {
		return ActionDeferredRequestValue{}, errors.New("types: not an action deferral request")
	}
	if req.Value == nil {
		return ActionDeferredRequestValue{}, errors.New("types: action deferral request has empty value")
	}
	return parseApprovalPayload[ActionDeferredRequestValue](req.Value)
}
