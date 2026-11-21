package temporal

import (
	"context"
	"errors"
	"fmt"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	agentrt "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// AgentActionSessionInput loads the ledger for one run.
type AgentActionSessionInput struct {
	AgentFingerprint string                `json:"agentFingerprint,omitempty"`
	RunID            string                `json:"runId"`
	Run              base.ActionRunContext `json:"run"`
}

// AgentActionGateActivityInput is one gate step.
type AgentActionGateActivityInput struct {
	AgentFingerprint string               `json:"agentFingerprint,omitempty"`
	Gate             base.ActionGateInput `json:"gate"`
}

// AgentActionRecordActivityInput is one record step.
type AgentActionRecordActivityInput struct {
	AgentFingerprint string                 `json:"agentFingerprint,omitempty"`
	Record           base.ActionRecordInput `json:"record"`
}

func (rt *TemporalRuntime) AgentActionSessionActivity(ctx context.Context, in AgentActionSessionInput) (actionpolicy.SessionState, error) {
	if err := rt.verifyAgentFingerprint(ctx, in.AgentFingerprint, nil); err != nil {
		return actionpolicy.SessionState{}, err
	}
	state, err := rt.LoadActionSession(ctx, in.RunID, in.Run)
	if err != nil {
		return actionpolicy.SessionState{}, actionPolicyActivityError(err)
	}
	return state, nil
}

func (rt *TemporalRuntime) AgentActionGateActivity(ctx context.Context, in AgentActionGateActivityInput) (base.ActionGateOutput, error) {
	if err := rt.verifyAgentFingerprint(ctx, in.AgentFingerprint, nil); err != nil {
		return base.ActionGateOutput{}, err
	}
	out, err := rt.GateActions(ctx, in.Gate)
	if err != nil {
		return base.ActionGateOutput{}, actionPolicyActivityError(err)
	}
	return out, nil
}

func (rt *TemporalRuntime) AgentActionRecordActivity(ctx context.Context, in AgentActionRecordActivityInput) (base.ActionRecordOutput, error) {
	if err := rt.verifyAgentFingerprint(ctx, in.AgentFingerprint, nil); err != nil {
		return base.ActionRecordOutput{}, err
	}
	out, err := rt.RecordActions(ctx, in.Record)
	if err != nil {
		return base.ActionRecordOutput{}, actionPolicyActivityError(err)
	}
	return out, nil
}

func actionPolicyActivityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, actionpolicy.ErrSessionConflict) || errors.Is(err, types.ErrActionPolicyUnavailable) {
		return temporal.NewNonRetryableApplicationError(err.Error(), "ActionPolicyUnavailable", err)
	}
	return err
}

func (rt *TemporalRuntime) prepareActionSession(ctx workflow.Context, input *AgentWorkflowInput, policies agentrt.ExecutionPolicies) error {
	if !rt.HasActionPolicy() || input.State == nil || input.State.ActionReady {
		return nil
	}
	run := base.ActionRunContext{Intent: input.UserPrompt, Task: input.UserPrompt, ConversationID: input.ConversationID}
	actCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.ActionPolicy, "AgentActionSessionActivity_"+input.RunID, false))
	var state actionpolicy.SessionState
	if err := workflow.ExecuteActivity(actCtx, rt.AgentActionSessionActivity, AgentActionSessionInput{
		AgentFingerprint: input.AgentFingerprint, RunID: input.RunID, Run: run,
	}).Get(actCtx, &state); err != nil {
		return err
	}
	input.State.ActionSession = state
	input.State.ActionLoadedHead = state.Head
	input.State.ActionReady = true
	return nil
}

func (rt *TemporalRuntime) gateActionBatch(
	ctx workflow.Context,
	input *AgentWorkflowInput,
	iteration int,
	calls []ToolCallRequest,
	policies agentrt.ExecutionPolicies,
	emit func(workflow.Context, events.AgentEvent) error,
) ([]base.ActionVerdict, error) {
	if !rt.HasActionPolicy() {
		return nil, nil
	}
	if err := rt.prepareActionSession(ctx, input, policies); err != nil {
		return nil, err
	}
	now := workflow.Now(ctx).UTC()
	gateCalls := make([]base.ActionCall, len(calls))
	for i, call := range calls {
		gateCalls[i] = base.ActionCall{
			ID: call.ToolCallID, Tool: call.ToolName, Kind: string(call.ToolKind),
			Args: call.Args, Unknown: call.Unknown, Iteration: iteration,
		}
	}
	actCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.ActionPolicy, fmt.Sprintf("AgentActionGateActivity_%s_%d", input.RunID, iteration), false))
	var out base.ActionGateOutput
	err := workflow.ExecuteActivity(actCtx, rt.AgentActionGateActivity, AgentActionGateActivityInput{
		AgentFingerprint: input.AgentFingerprint,
		Gate: base.ActionGateInput{
			RunID: input.RunID, Now: now, Calls: gateCalls, Session: input.State.ActionSession,
			Run:              base.ActionRunContext{Intent: input.UserPrompt, Task: input.UserPrompt, ConversationID: input.ConversationID},
			DeferralsThisRun: input.State.ActionDeferrals,
		},
	}).Get(actCtx, &out)
	if err != nil {
		return nil, err
	}
	input.State.ActionDeferrals = out.DeferralsThisRun
	for _, verdict := range out.Verdicts {
		if err := emit(ctx, base.DecisionEvent(verdict)); err != nil {
			return nil, err
		}
	}
	return out.Verdicts, nil
}

func (rt *TemporalRuntime) recordActionBatch(
	ctx workflow.Context,
	input *AgentWorkflowInput,
	iteration int,
	verdicts []base.ActionVerdict,
	results []agentToolResult,
	policies agentrt.ExecutionPolicies,
) error {
	if len(verdicts) == 0 {
		return nil
	}
	now := workflow.Now(ctx).UTC()
	completed := make([]base.ActionCompletion, len(verdicts))
	for i, verdict := range verdicts {
		status := actionpolicy.OutcomeExecuted
		cause := ""
		if verdict.Result == actionpolicy.ResultDeny {
			status = actionpolicy.OutcomeNotExecuted
			cause = verdict.Cause
		} else if i < len(results) && results[i].failed {
			status = actionpolicy.OutcomeFailed
		}
		content := ""
		if i < len(results) {
			content = results[i].message.Content
		}
		completed[i] = base.ActionCompletion{
			ToolCallID: verdict.ToolCallID, Status: status, Cause: cause,
			ResultDigest: actionpolicy.DigestText(content),
		}
	}
	actCtx := workflow.WithActivityOptions(ctx, execActivityOptions(policies.ActionPolicy, fmt.Sprintf("AgentActionRecordActivity_%s_%d", input.RunID, iteration), false))
	var out base.ActionRecordOutput
	err := workflow.ExecuteActivity(actCtx, rt.AgentActionRecordActivity, AgentActionRecordActivityInput{
		AgentFingerprint: input.AgentFingerprint,
		Record: base.ActionRecordInput{
			RunID: input.RunID, Now: now, Session: input.State.ActionSession,
			Run:      base.ActionRunContext{Intent: input.UserPrompt, Task: input.UserPrompt, ConversationID: input.ConversationID},
			Verdicts: verdicts, Completed: completed, LoadedHead: input.State.ActionLoadedHead,
		},
	}).Get(actCtx, &out)
	if err != nil {
		return err
	}
	input.State.ActionSession = out.Session
	input.State.ActionLoadedHead = out.LoadedHead
	input.State.ActionDenied += out.Summary.Denied
	input.State.ActionDeferred += out.Summary.Deferred
	return nil
}

func actionRunSummary(input AgentWorkflowInput) *types.ActionSummary {
	if input.State == nil || !input.State.ActionReady {
		return nil
	}
	summary := base.SummaryFrom(input.State.ActionSession, input.RunID)
	summary.Denied = input.State.ActionDenied
	summary.Deferred = input.State.ActionDeferred
	return &summary
}

func actionVerdictAt(verdicts []base.ActionVerdict, idx int) *base.ActionVerdict {
	if idx < 0 || idx >= len(verdicts) {
		return nil
	}
	return &verdicts[idx]
}

func applyActionVerdict(tc ToolCallRequest, verdict *base.ActionVerdict) ToolCallRequest {
	if verdict == nil {
		return tc
	}
	if verdict.Result == actionpolicy.ResultStepUp || verdict.Result == actionpolicy.ResultDefer {
		tc.NeedsApproval = true
	}
	return tc
}
