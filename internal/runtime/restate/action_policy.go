package restate

import (
	"fmt"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	restatesdk "github.com/restatedev/sdk-go"
)

type restateActionLedger struct {
	session    actionpolicy.SessionState
	deferrals  int
	loadedHead string
	summary    types.ActionSummary
	ready      bool
}

func (rt *RestateRuntime) executeToolsWithActionPolicy(
	ctx restatesdk.Context,
	input *AgentLoopInput,
	messageID string,
	iteration int,
	calls []base.ToolCallRequest,
	policies sdkruntime.ExecutionPolicies,
	emit func(events.AgentEvent),
) ([]toolResult, error) {
	if err := rt.ensureRestateActionSession(ctx, input, policies); err != nil {
		return nil, err
	}
	now, err := executeWithPolicy(ctx, fmt.Sprintf("action-now-%d", iteration), policies.ActionPolicy, func(restatesdk.RunContext) (time.Time, error) {
		return time.Now().UTC(), nil
	})
	if err != nil {
		return nil, err
	}
	gateCalls := make([]base.ActionCall, len(calls))
	for i, call := range calls {
		gateCalls[i] = base.ActionCall{
			ID: call.ToolCallID, Tool: call.ToolName, Kind: string(call.ToolKind),
			Args: call.Args, Unknown: call.Unknown, Iteration: iteration,
		}
	}
	out, err := executeWithPolicy(ctx, fmt.Sprintf("action-gate-%d", iteration), policies.ActionPolicy, func(runCtx restatesdk.RunContext) (base.ActionGateOutput, error) {
		return rt.GateActions(runCtx, base.ActionGateInput{
			RunID: input.RunID, Now: now, Calls: gateCalls, Session: input.actionLedger.session,
			Run:              base.ActionRunContext{Intent: input.UserPrompt, Task: input.UserPrompt, ConversationID: input.ConversationID},
			DeferralsThisRun: input.actionLedger.deferrals,
		})
	})
	if err != nil {
		return nil, err
	}
	input.actionLedger.deferrals = out.DeferralsThisRun
	for _, verdict := range out.Verdicts {
		emit(base.DecisionEvent(verdict))
	}
	results := make([]toolResult, len(calls))
	for i, call := range calls {
		call = restateApplyVerdict(call, out.Verdicts[i])
		res, execErr := rt.executeSingleTool(ctx, *input, messageID, iteration, call, policies, emit, &out.Verdicts[i])
		if execErr != nil {
			res = toolResult{message: interfaces.Message{
				Role: interfaces.MessageRoleTool, Content: "Tool execution failed: " + execErr.Error(),
				ToolName: call.ToolName, ToolCallID: call.ToolCallID,
			}, failed: true}
		}
		results[i] = res
	}
	recorded, err := executeWithPolicy(ctx, fmt.Sprintf("action-record-%d", iteration), policies.ActionPolicy, func(runCtx restatesdk.RunContext) (base.ActionRecordOutput, error) {
		completed := make([]base.ActionCompletion, len(out.Verdicts))
		for i, verdict := range out.Verdicts {
			status := actionpolicy.OutcomeExecuted
			cause := ""
			if verdict.Result == actionpolicy.ResultDeny {
				status = actionpolicy.OutcomeNotExecuted
				cause = verdict.Cause
			} else if results[i].failed {
				status = actionpolicy.OutcomeFailed
			}
			completed[i] = base.ActionCompletion{
				ToolCallID: verdict.ToolCallID, Status: status, Cause: cause,
				ResultDigest: actionpolicy.DigestText(results[i].message.Content),
			}
		}
		return rt.RecordActions(runCtx, base.ActionRecordInput{
			RunID: input.RunID, Now: now, Session: input.actionLedger.session,
			Run:      base.ActionRunContext{Intent: input.UserPrompt, Task: input.UserPrompt, ConversationID: input.ConversationID},
			Verdicts: out.Verdicts, Completed: completed, LoadedHead: input.actionLedger.loadedHead,
		})
	})
	if err != nil {
		return nil, err
	}
	input.actionLedger.session = recorded.Session
	input.actionLedger.loadedHead = recorded.LoadedHead
	input.actionLedger.summary.SessionID = recorded.Summary.SessionID
	input.actionLedger.summary.LedgerHead = recorded.Summary.LedgerHead
	input.actionLedger.summary.Count = recorded.Summary.Count
	input.actionLedger.summary.Labels = recorded.Summary.Labels
	input.actionLedger.summary.Denied += recorded.Summary.Denied
	input.actionLedger.summary.Deferred += recorded.Summary.Deferred
	return results, nil
}

func (rt *RestateRuntime) ensureRestateActionSession(ctx restatesdk.Context, input *AgentLoopInput, policies sdkruntime.ExecutionPolicies) error {
	if input.actionLedger != nil && input.actionLedger.ready {
		return nil
	}
	state, err := executeWithPolicy(ctx, "action-session", policies.ActionPolicy, func(runCtx restatesdk.RunContext) (actionpolicy.SessionState, error) {
		return rt.LoadActionSession(runCtx, input.RunID, base.ActionRunContext{
			Intent: input.UserPrompt, Task: input.UserPrompt, ConversationID: input.ConversationID,
		})
	})
	if err != nil {
		return err
	}
	input.actionLedger = &restateActionLedger{session: state, loadedHead: state.Head, ready: true}
	return nil
}

func restateActionSummary(input AgentLoopInput) *types.ActionSummary {
	if input.actionLedger == nil || !input.actionLedger.ready {
		return nil
	}
	summary := input.actionLedger.summary
	return &summary
}

func restateApplyVerdict(tc base.ToolCallRequest, verdict base.ActionVerdict) base.ToolCallRequest {
	if verdict.Result == actionpolicy.ResultStepUp || verdict.Result == actionpolicy.ResultDefer {
		tc.NeedsApproval = true
	}
	return tc
}
