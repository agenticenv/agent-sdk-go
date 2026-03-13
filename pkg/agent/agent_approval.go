package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

type ApprovalStatus string

const (
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

// ApprovalRequest contains the details of a pending tool approval.
// Sent via in-process channel when a tool requires approval.
type ApprovalRequest struct {
	WorkflowID string         `json:"workflow_id"`
	RunID      string         `json:"run_id"`
	TaskToken  []byte         `json:"task_token"` // base64 in JSON
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args"`
}

// subscribeToApprovals returns a channel that receives ApprovalRequest from the per-run approval channel.
func (a *Agent) subscribeToApprovals(ctx context.Context, runID string) (<-chan *ApprovalRequest, func() error, error) {
	channel := toolRunApprovalChannelPrefix + runID
	ch, closeFn, err := a.messaging.Subscribe(ctx, channel)
	if err != nil {
		return nil, nil, err
	}

	reqCh := make(chan *ApprovalRequest)
	go func() {
		defer close(reqCh)
		for data := range ch {
			var req ApprovalRequest
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			reqCh <- &req
		}
	}()

	return reqCh, closeFn, nil
}
