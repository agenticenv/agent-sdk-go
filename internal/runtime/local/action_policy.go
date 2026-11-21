package local

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	durable "github.com/agenticenv/durable-go"
)

// actionToolOutput is the durable result of one tool call under an action policy.
type actionToolOutput struct {
	Content            string
	Status             string
	Cause              string
	ResultDigest       string
	DeliveredDigest    string
	ExecutedArgsDigest string
	ResultLabels       []string
	Recheck            actionpolicy.Result
}

// actionLedger is the session advanced by this run's gate and record steps.
type actionLedger struct {
	session    actionpolicy.SessionState
	deferrals  int
	loadedHead string
	summary    types.ActionSummary
}

func actionSummary(input AgentLoopInput) *types.ActionSummary {
	if input.actionLedger == nil {
		return nil
	}
	summary := input.actionLedger.summary
	return &summary
}

func (rt *LocalRuntime) loadActionLedger(ctx context.Context, input *AgentLoopInput) error {
	if !rt.HasActionPolicy() {
		return nil
	}
	if input.Action.Intent == "" {
		input.Action.Intent = input.UserPrompt
	}
	if input.Action.Task == "" {
		input.Action.Task = input.UserPrompt
	}
	input.Action.ConversationID = input.ConversationID
	state, err := runStep(ctx, input.stepRunner, input.stepPrefix+"action-session", func(stepCtx context.Context) (actionpolicy.SessionState, error) {
		return executeWithPolicy(stepCtx, rt.executionPolicies().ActionPolicy, func(attemptCtx context.Context) (actionpolicy.SessionState, error) {
			return rt.LoadActionSession(attemptCtx, input.RunID, input.Action)
		})
	})
	if err != nil {
		return err
	}
	input.actionLedger = &actionLedger{session: state, loadedHead: state.Head}
	return nil
}

func (rt *LocalRuntime) gateActionCalls(
	ctx context.Context,
	input AgentLoopInput,
	iteration int,
	calls []base.ToolCallRequest,
	stepID string,
	emit func(events.AgentEvent),
) ([]base.ActionVerdict, error) {
	now := time.Now().UTC()
	out, err := runStep(ctx, input.stepRunner, stepID, func(stepCtx context.Context) (base.ActionGateOutput, error) {
		return executeWithPolicy(stepCtx, rt.executionPolicies().ActionPolicy, func(attemptCtx context.Context) (base.ActionGateOutput, error) {
			return rt.GateActions(attemptCtx, base.ActionGateInput{
				RunID:            input.RunID,
				Now:              now,
				Calls:            actionCalls(calls, iteration),
				Session:          input.actionLedger.session,
				Run:              input.Action,
				DeferralsThisRun: input.actionLedger.deferrals,
			})
		})
	})
	if err != nil {
		return nil, err
	}
	input.actionLedger.deferrals = out.DeferralsThisRun
	for _, verdict := range out.Verdicts {
		emit(base.DecisionEvent(verdict))
	}
	return out.Verdicts, nil
}

func (rt *LocalRuntime) recordActionCalls(
	ctx context.Context,
	input AgentLoopInput,
	verdicts []base.ActionVerdict,
	results []toolResult,
	stepID string,
) error {
	now := time.Now().UTC()
	completed := make([]base.ActionCompletion, len(verdicts))
	for i, verdict := range verdicts {
		completed[i] = results[i].completion
		if completed[i].ToolCallID == "" {
			completed[i].ToolCallID = verdict.ToolCallID
		}
		if completed[i].Status == "" {
			completed[i].Status = actionpolicy.OutcomeExecuted
			if results[i].failed {
				completed[i].Status = actionpolicy.OutcomeFailed
			}
			if completed[i].ResultDigest == "" {
				completed[i].ResultDigest = actionpolicy.DigestText(results[i].message.Content)
			}
		}
	}
	out, err := runStep(ctx, input.stepRunner, stepID, func(stepCtx context.Context) (base.ActionRecordOutput, error) {
		return executeWithPolicy(stepCtx, rt.executionPolicies().ActionPolicy, func(attemptCtx context.Context) (base.ActionRecordOutput, error) {
			return rt.RecordActions(attemptCtx, base.ActionRecordInput{
				RunID:      input.RunID,
				Now:        now,
				Session:    input.actionLedger.session,
				Run:        input.Action,
				Verdicts:   verdicts,
				Completed:  completed,
				LoadedHead: input.actionLedger.loadedHead,
			})
		})
	})
	if err != nil {
		return err
	}
	input.actionLedger.session = out.Session
	input.actionLedger.loadedHead = out.LoadedHead
	input.actionLedger.summary.SessionID = out.Summary.SessionID
	input.actionLedger.summary.LedgerHead = out.Summary.LedgerHead
	input.actionLedger.summary.Count = out.Summary.Count
	input.actionLedger.summary.Labels = append([]string(nil), out.Summary.Labels...)
	input.actionLedger.summary.Denied += out.Summary.Denied
	input.actionLedger.summary.Deferred += out.Summary.Deferred
	return nil
}

func actionCalls(calls []base.ToolCallRequest, iteration int) []base.ActionCall {
	out := make([]base.ActionCall, len(calls))
	for i, call := range calls {
		out[i] = base.ActionCall{
			ID: call.ToolCallID, Tool: call.ToolName, Kind: string(call.ToolKind),
			Args: call.Args, Unknown: call.Unknown, Iteration: iteration,
		}
	}
	return out
}

func verdictAt(verdicts []base.ActionVerdict, idx int) *base.ActionVerdict {
	if idx < 0 || idx >= len(verdicts) {
		return nil
	}
	return &verdicts[idx]
}

func (rt *LocalRuntime) actionObservation(input AgentLoopInput, tc base.ToolCallRequest, iteration int, verdict *base.ActionVerdict) *base.ActionObservation {
	obs := &base.ActionObservation{GatedDigest: verdict.ArgsDigest}
	if len(rt.AgentConfig.Hooks) == 0 {
		return obs
	}
	obs.Recheck = func(ctx context.Context, args map[string]any) (actionpolicy.Result, string, error) {
		session := actionpolicy.SessionState{}
		if input.actionLedger != nil {
			session = input.actionLedger.session
		}
		return rt.RecheckAction(ctx, base.ActionGateInput{RunID: input.RunID, Run: input.Action}, session, base.ActionCall{
			ID: tc.ToolCallID, Tool: tc.ToolName, Kind: string(tc.ToolKind), Args: args, Iteration: iteration,
		}, args)
	}
	return obs
}

func childActionContext(input AgentLoopInput, agentID, agentName string, call base.ToolCallRequest, query string) base.ActionRunContext {
	child := input.Action
	child.Task = query
	child.ConversationID = ""
	if input.actionLedger != nil {
		child.InheritedLabels = append([]string(nil), input.actionLedger.session.Labels...)
	}
	child.Delegation = append(append([]actionpolicy.Hop(nil), input.Action.Delegation...), actionpolicy.Hop{
		AgentID: agentID, AgentName: agentName, RunID: input.RunID, ActionID: call.ToolCallID,
	})
	return child
}

func (rt *LocalRuntime) deferralTimeout() time.Duration {
	cfg := rt.AgentConfig.ActionPolicy.Resolved()
	if cfg.DeferTimeout > 0 {
		return cfg.DeferTimeout
	}
	return rt.approvalTaskTimeout()
}

// waitActionDeferral pauses until the deferral is resolved.
func (rt *LocalRuntime) waitActionDeferral(
	ctx context.Context,
	input AgentLoopInput,
	iteration, idx int,
	tc base.ToolCallRequest,
	verdict *base.ActionVerdict,
) (types.ActionResolution, error) {
	timeout := rt.deferralTimeout()
	if input.stepRunner != nil {
		return rt.durableDeferralWait(ctx, input, iteration, idx, tc, verdict, timeout)
	}
	if input.ChannelName == "" && input.ApprovalHandler == nil {
		return types.ActionResolution{Status: types.ApprovalStatusUnavailable, Method: actionpolicy.MethodUnavailable}, nil
	}
	token := base.PrefixActionToken(fmt.Sprintf("%s-%d-%d", input.RunID, iteration, idx))
	ch := make(chan types.ActionResolution, 1)
	rt.pendingResolutions.Store(token, ch)
	defer rt.pendingResolutions.Delete(token)
	rt.publishDeferral(ctx, input, tc, token, verdict, timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return types.ActionResolution{}, ctx.Err()
	case <-timer.C:
		return types.ActionResolution{Status: types.ApprovalStatusTimedOut, Method: actionpolicy.MethodTimeout}, nil
	}
}

func (rt *LocalRuntime) durableDeferralWait(
	ctx context.Context,
	input AgentLoopInput,
	iteration, idx int,
	tc base.ToolCallRequest,
	verdict *base.ActionVerdict,
	timeout time.Duration,
) (types.ActionResolution, error) {
	stepID := fmt.Sprintf("%saction-defer-%d-%d", input.stepPrefix, iteration, idx)
	var rawToken string
	resolution, err := runStep(ctx, input.stepRunner, stepID, func(stepCtx context.Context) (types.ActionResolution, error) {
		rawToken = input.stepRunner.StepToken(stepCtx)
		token := base.PrefixActionToken(rawToken)
		rt.publishDeferral(stepCtx, input, tc, token, verdict, timeout)
		return types.ActionResolution{}, durable.ErrStepPending
	}, durable.WithStepTimeout(timeout), durable.WithStepTokenTTL(timeout))
	if err != nil && errors.Is(err, context.DeadlineExceeded) && rawToken != "" {
		res := types.ActionResolution{Status: types.ApprovalStatusTimedOut, Method: actionpolicy.MethodTimeout}
		if completeErr := durable.CompleteStep(context.Background(), rt.engine, rawToken, res); completeErr != nil &&
			!errors.Is(completeErr, durable.ErrRunAlreadyFinished) {
			rt.logger.Warn(ctx, "local: failed to self-resolve timed-out action deferral",
				slog.String("scope", "loop"),
				slog.String("stepID", stepID),
				slog.Any("error", completeErr))
		}
		return res, nil
	}
	return resolution, err
}

func (rt *LocalRuntime) publishDeferral(
	ctx context.Context,
	input AgentLoopInput,
	tc base.ToolCallRequest,
	token string,
	verdict *base.ActionVerdict,
	timeout time.Duration,
) {
	rt.publishEventToChannel(ctx, input.ChannelName, actionDeferredEvent(rt.AgentSpec.Name, tc, token, verdict))
	if input.ApprovalHandler == nil {
		return
	}
	req := actionDeferredRequest(rt.AgentSpec.Name, tc, token, verdict, input)
	req.Resolve = func(res types.ActionResolution) error { return rt.resolve(context.Background(), token, res) }
	req.Respond = func(status types.ApprovalStatus) error {
		return req.Resolve(types.ActionResolution{Status: status, Method: actionpolicy.MethodHuman})
	}
	go func() {
		handlerCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		input.ApprovalHandler(handlerCtx, req)
	}()
}

func actionDeferredEvent(agentName string, tc base.ToolCallRequest, token string, verdict *base.ActionVerdict) events.AgentEvent {
	return events.NewAgentCustomEvent(string(events.AgentCustomEventNameActionDeferred), types.ActionDeferredRequestValue{
		AgentName: agentName, ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
		ToolDisplayName: tc.ToolDisplayName, Args: tc.Args, ApprovalToken: token,
		Reason: verdict.Reason, Rule: verdict.Rule,
	})
}

func actionDeferredRequest(agentName string, tc base.ToolCallRequest, token string, verdict *base.ActionVerdict, input AgentLoopInput) *types.ApprovalRequest {
	return &types.ApprovalRequest{
		Name: types.ApprovalRequestNameDeferral,
		Value: types.ActionDeferredRequestValue{
			AgentName: agentName, ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
			ToolDisplayName: tc.ToolDisplayName, Args: tc.Args, ApprovalToken: token,
			Reason: verdict.Reason, Rule: verdict.Rule,
			Policy: &types.ActionApprovalInfo{
				Decision: verdict.Result, Reason: verdict.Reason, Rule: verdict.Rule,
				Intent: input.Action.Intent, Requester: input.Action.Requester, Labels: verdict.Labels,
			},
		},
	}
}

func policyDeniedResult(verdict *base.ActionVerdict, tc base.ToolCallRequest, content, cause string) toolResult {
	return toolResult{
		message: interfaces.Message{
			Role:       interfaces.MessageRoleTool,
			Content:    content,
			ToolName:   tc.ToolName,
			ToolCallID: tc.ToolCallID,
		},
		completion: base.ActionCompletion{
			ToolCallID:   verdict.ToolCallID,
			Status:       actionpolicy.OutcomeNotExecuted,
			Cause:        cause,
			ResultDigest: actionpolicy.DigestText(content),
		},
	}
}

func deferralCause(res types.ActionResolution) (cause, note string) {
	switch res.Status {
	case types.ApprovalStatusRejected:
		return actionpolicy.CauseDeferralRejected, res.Note
	case types.ApprovalStatusTimedOut:
		return actionpolicy.CauseDeferralTimeout, ""
	case types.ApprovalStatusUnavailable:
		return actionpolicy.CauseApprovalUnavailable, ""
	default:
		return actionpolicy.CauseDeferralRejected, res.Note
	}
}

var _ sdkruntime.ExecutionPolicy
var _ = fmt.Sprintf
