package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
)

type ApprovalStatus string

const (
	ApprovalStatusNone     ApprovalStatus = "NONE"
	ApprovalStatusPending  ApprovalStatus = "PENDING"
	ApprovalStatusApproved ApprovalStatus = "APPROVED"
	ApprovalStatusRejected ApprovalStatus = "REJECTED"
)

// ErrApprovalRequired is returned by AgentLLMActivity when a tool needs approval and the workflow should run ToolApprovalActivity.
var ErrApprovalRequired = &errApprovalRequired{}

type errApprovalRequired struct {
	ToolName string
	Args     map[string]any
}

func (e *errApprovalRequired) Error() string {
	return fmt.Sprintf("approval required for tool %s", e.ToolName)
}

// ApprovalSender sends an approval result. Call once per request. Safe for concurrent use—
// multiple approvals may be pending when tools run in parallel.
type ApprovalSender func(status ApprovalStatus) error

// ApprovalHandler is called when a tool needs approval. Call send with the result when ready.
// The handler may return immediately after starting async work. Multiple handlers may run
// concurrently when tools are invoked in parallel.
type ApprovalHandler func(ctx context.Context, req *ApprovalRequest, onApproval ApprovalSender)

// ApprovalRequest contains the user-facing details of a pending tool approval.
// Only ToolName and Args are exposed; workflow ID and task token are internal.
type ApprovalRequest struct {
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
}

// approvalRequest is the internal wire format; embeds ApprovalRequest and adds fields for completion and routing.
type approvalRequest struct {
	ApprovalRequest
	AgentWorkflowID string `json:"agent_workflow_id"`
	TaskToken       []byte `json:"task_token"`
}

// subscribeToApprovals returns a channel that receives approvalRequest from the per-run approval channel.
func (a *Agent) subscribeToApprovals(ctx context.Context, workflowID string) (<-chan *approvalRequest, func() error, error) {
	channel := approvalChannelName(workflowID)

	a.logger.Debug("subscribing to approvals", zap.String("channel", channel))

	ch, closeFn, err := a.agentChannel.Subscribe(ctx, channel)
	if err != nil {
		a.logger.Error("error subscribing to approvals", zap.Error(err))
		return nil, nil, err
	}

	reqCh := make(chan *approvalRequest)
	go func() {
		defer close(reqCh)
		for data := range ch {
			var req approvalRequest
			if err := json.Unmarshal(data, &req); err != nil {
				a.logger.Debug("failed to unmarshal approval request", zap.Error(err))
				continue
			}
			reqCh <- &req
		}
	}()

	a.logger.Debug("subscribed to approvals", zap.String("channel", channel))
	return reqCh, closeFn, nil
}
