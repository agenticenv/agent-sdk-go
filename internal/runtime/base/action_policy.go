package base

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// ActionPolicyTokenPrefix marks a pause token issued while an action policy is set.
const ActionPolicyTokenPrefix = "ar1:"

// PrefixActionToken marks token as an action-policy pause token.
func PrefixActionToken(token string) string {
	if strings.HasPrefix(token, ActionPolicyTokenPrefix) {
		return token
	}
	return ActionPolicyTokenPrefix + token
}

// ActionTokenRaw reports whether token is an action-policy pause token and returns the runtime token.
func ActionTokenRaw(token string) (string, bool) {
	return strings.CutPrefix(token, ActionPolicyTokenPrefix)
}

// ActionRunContext is the caller and delegation context carried through one run.
type ActionRunContext struct {
	Intent          string
	Task            string
	ConversationID  string
	Requester       actionpolicy.Requester
	Delegation      []actionpolicy.Hop
	InheritedLabels []string
}

// ActionCall is one model-emitted tool call presented to the gate.
type ActionCall struct {
	ID        string
	Tool      string
	Kind      string
	Args      map[string]any
	Unknown   bool
	Iteration int
}

// ActionVerdict is the signed decision for one call.
type ActionVerdict struct {
	ToolCallID  string
	Tool        string
	Kind        string
	Result      actionpolicy.Result
	Evaluated   actionpolicy.Result
	Reason      string
	Rule        string
	Args        map[string]any
	Labels      []string
	ReceiptID   string
	ReceiptHash string
	ArgsDigest  string
	Cause       string
	IssuedDefer bool
	// SkipApproval is set when a human approved a deferral, so tool approval is not asked again.
	SkipApproval bool
}

// ActionGateInput is one gate step. Now is the loop clock so a retry signs the same bytes.
type ActionGateInput struct {
	RunID            string
	Now              time.Time
	Calls            []ActionCall
	Session          actionpolicy.SessionState
	Run              ActionRunContext
	AgentName        string
	DeferralsThisRun int
}

// ActionGateOutput is the decisions for the batch, in call order.
type ActionGateOutput struct {
	Verdicts         []ActionVerdict
	DeferralsThisRun int
}

// ActionCompletion is what happened after a decision.
type ActionCompletion struct {
	ToolCallID         string
	Status             string
	Cause              string
	ExecutedArgsDigest string
	ResultDigest       string
	DeliveredDigest    string
	ResultLabels       []string
	Error              string
	Recheck            actionpolicy.Result
	Approval           *actionpolicy.ReceiptApproval
	Deferral           *actionpolicy.ReceiptDeferral
	Child              *actionpolicy.ReceiptChild
	ChildLabels        []string
}

// ActionRecordInput appends outcome receipts for one batch.
// LoadedHead is the session head observed by the matching load, used for the store conflict check.
type ActionRecordInput struct {
	RunID      string
	Now        time.Time
	Session    actionpolicy.SessionState
	Run        ActionRunContext
	AgentName  string
	Verdicts   []ActionVerdict
	Completed  []ActionCompletion
	LoadedHead string
}

// ActionRecordOutput is the session after the batch is recorded.
type ActionRecordOutput struct {
	Session    actionpolicy.SessionState
	LoadedHead string
	Summary    types.ActionSummary
}

// HasActionPolicy reports whether this runtime signs tool calls.
func (rt *Runtime) HasActionPolicy() bool {
	return rt.AgentConfig.ActionPolicy != nil && rt.AgentConfig.ActionPolicy.Policy != nil
}

// LoadActionSession loads the conversation ledger, or starts a fresh one keyed by the run.
// Inherited labels are added to the label set for this run and are not ledger entries.
func (rt *Runtime) LoadActionSession(ctx context.Context, runID string, run ActionRunContext) (actionpolicy.SessionState, error) {
	cfg := rt.AgentConfig.ActionPolicy.Resolved()
	state := actionpolicy.SessionState{ID: runID}
	if cfg.SessionStore != nil && run.ConversationID != "" {
		loaded, err := cfg.SessionStore.Load(ctx, run.ConversationID)
		if err != nil {
			return actionpolicy.SessionState{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
		}
		state.ID = run.ConversationID
		if loaded != nil {
			state = *loaded
			if state.ID == "" {
				state.ID = run.ConversationID
			}
		}
	}
	state.Labels = actionpolicy.UnionLabels(state.Labels, run.InheritedLabels)
	return state, nil
}

// GateActions classifies, evaluates, and signs one decision receipt per call.
// Only signer and sink failures are returned. A policy error denies that call.
func (rt *Runtime) GateActions(ctx context.Context, in ActionGateInput) (ActionGateOutput, error) {
	cfg := rt.AgentConfig.ActionPolicy.Resolved()
	start := time.Now()
	defer func() {
		rt.Metrics.RecordHistogram(ctx, types.MetricActionPolicyGateMs, float64(time.Since(start).Milliseconds()))
	}()

	working := in.Session
	pending := make([]actionpolicy.PriorAction, 0, len(in.Calls))
	out := ActionGateOutput{DeferralsThisRun: in.DeferralsThisRun}
	for _, call := range in.Calls {
		verdict, err := rt.gateOne(ctx, cfg, in, &working, pending, call, &out.DeferralsThisRun)
		if err != nil {
			rt.Metrics.IncrementCounter(ctx, types.MetricActionPolicyUnavailable)
			return ActionGateOutput{}, err
		}
		out.Verdicts = append(out.Verdicts, verdict)
		pending = append(pending, actionpolicy.PriorAction{
			Seq:        in.Session.Count + len(pending) + 1,
			Tool:       call.Tool,
			Kind:       call.Kind,
			ArgsDigest: verdict.ArgsDigest,
			Decision:   verdict.Result,
			Outcome:    pendingOutcome(verdict.Result),
			Labels:     verdict.Labels,
		})
		working.Labels = actionpolicy.UnionLabels(working.Labels, verdict.Labels)
		rt.noteDecision(ctx, verdict)
	}
	return out, nil
}

func (rt *Runtime) gateOne(ctx context.Context, cfg actionpolicy.Config, in ActionGateInput, working *actionpolicy.SessionState, pending []actionpolicy.PriorAction, call ActionCall, deferrals *int) (ActionVerdict, error) {
	labels := rt.classifyAction(ctx, cfg, call)
	decisionArgs := call.Args
	var (
		result    = actionpolicy.ResultDeny
		evaluated actionpolicy.Result
		reason    string
		rule      string
		cause     string
	)
	if call.Unknown {
		rule = actionpolicy.RuleUnknownTool
		reason = "unknown tool"
		cause = actionpolicy.CauseUnknownTool
	} else {
		dec, evalErr := rt.evaluatePolicy(ctx, cfg, in, *working, pending, call, labels, false, nil)
		if evalErr != nil {
			return ActionVerdict{}, evalErr
		}
		result = dec.Result
		evaluated = dec.Result
		reason = dec.Reason
		rule = dec.Rule
		if !result.Known() || (result == actionpolicy.ResultModify && dec.Args == nil) {
			result = actionpolicy.ResultDeny
			rule = actionpolicy.RulePolicyError
			reason = "policy returned an invalid decision"
		}
		if result == actionpolicy.ResultDefer && *deferrals >= cfg.MaxDeferrals {
			result = actionpolicy.ResultDeny
			cause = actionpolicy.CauseDeferLimit
			reason = "deferral limit exceeded"
		}
		if result == actionpolicy.ResultModify {
			decisionArgs = dec.Args
		}
		if result == actionpolicy.ResultDeny && cause == "" && evaluated != actionpolicy.ResultDefer {
			cause = actionpolicy.CausePolicyDenied
		}
	}
	stored, digest, err := actionpolicy.RedactedArgs(cfg.RedactArgs, call.Tool, decisionArgs)
	if err != nil {
		return ActionVerdict{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
	}
	body := rt.decisionReceipt(cfg, in, *working, pending, call, stored, digest, result, evaluated, reason, rule)
	receiptID := actionpolicy.ReceiptID(in.RunID, call.ID, actionpolicy.ReceiptTypeDecision)
	signed, err := actionpolicy.Sign(ctx, cfg.Signer, receiptID, actionpolicy.ReceiptTypeDecision, body)
	if err != nil {
		return ActionVerdict{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
	}
	if err := cfg.Sink.Write(ctx, signed); err != nil {
		return ActionVerdict{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
	}
	issued := result == actionpolicy.ResultDefer
	if issued {
		*deferrals++
	}
	verdict := ActionVerdict{
		ToolCallID:  call.ID,
		Tool:        call.Tool,
		Kind:        call.Kind,
		Result:      result,
		Evaluated:   evaluated,
		Reason:      reason,
		Rule:        rule,
		Labels:      labels,
		ReceiptID:   signed.ID,
		ReceiptHash: signed.Hash(),
		ArgsDigest:  digest,
		Cause:       cause,
		IssuedDefer: issued,
	}
	if result == actionpolicy.ResultModify {
		verdict.Args = decisionArgs
	}
	return verdict, nil
}

func (rt *Runtime) classifyAction(ctx context.Context, cfg actionpolicy.Config, call ActionCall) []string {
	if cfg.Classifier == nil || call.Unknown {
		return nil
	}
	labels, err := cfg.Classifier.ClassifyAction(ctx, actionpolicy.Action{
		ID: call.ID, Tool: call.Tool, Kind: call.Kind, Args: call.Args, RunID: "", Iteration: call.Iteration,
	})
	if err != nil {
		if len(labels) > 0 {
			return labels
		}
		return []string{"restricted"}
	}
	return labels
}

func (rt *Runtime) evaluatePolicy(ctx context.Context, cfg actionpolicy.Config, in ActionGateInput, session actionpolicy.SessionState, pending []actionpolicy.PriorAction, call ActionCall, labels []string, recheck bool, args map[string]any) (actionpolicy.Decision, error) {
	if args == nil {
		args = call.Args
	}
	req := actionpolicy.Request{
		Action: actionpolicy.Action{
			ID: call.ID, Tool: call.Tool, Kind: call.Kind, Args: args, Labels: labels,
			RunID: in.RunID, Iteration: call.Iteration, Recheck: recheck,
		},
		Session:  sessionView(in, session, pending),
		Identity: agentIdentity(cfg, in, rt),
	}
	evalCtx, cancel := evaluationContext(ctx, cfg.EvaluateTimeout)
	defer cancel()
	dec, err := evaluateCall(evalCtx, cfg.Policy, req)
	if ctx.Err() != nil {
		return actionpolicy.Decision{}, ctx.Err()
	}
	if evalCtx.Err() != nil {
		return actionpolicy.Decision{Result: actionpolicy.ResultDeny, Rule: actionpolicy.RulePolicyError, Reason: "policy evaluation timed out"}, nil
	}
	if err != nil {
		return actionpolicy.Decision{Result: actionpolicy.ResultDeny, Rule: actionpolicy.RulePolicyError, Reason: err.Error()}, nil
	}
	return dec, nil
}

func evaluateCall(ctx context.Context, policy actionpolicy.Policy, req actionpolicy.Request) (dec actionpolicy.Decision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("policy panic: %v", recovered)
		}
	}()
	return policy.Evaluate(ctx, req)
}

func evaluationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return parent, func() {}
		}
		half := remaining / 2
		if timeout <= 0 || half < timeout {
			timeout = half
		}
	}
	if timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}

func (rt *Runtime) decisionReceipt(cfg actionpolicy.Config, in ActionGateInput, session actionpolicy.SessionState, pending []actionpolicy.PriorAction, call ActionCall, stored map[string]any, digest string, result, evaluated actionpolicy.Result, reason, rule string) actionpolicy.Receipt {
	decision := actionpolicy.ReceiptDecision{
		Result: result, PolicyID: cfg.Policy.ID(), PolicyVersion: cfg.Policy.Version(), PolicyHash: cfg.Policy.Hash(),
		Rule: rule, Reason: reason,
	}
	if evaluated != "" && evaluated != result {
		decision.EvaluatedResult = evaluated
	}
	return actionpolicy.Receipt{
		Action: actionpolicy.ReceiptAction{
			ID: call.ID, Tool: call.Tool, Kind: call.Kind, Args: stored, ArgsDigest: digest, Timestamp: in.Now,
		},
		Context: actionpolicy.ReceiptContext{
			SessionID: session.ID, ConversationID: in.Run.ConversationID,
			IntentDigest: actionpolicy.DigestText(in.Run.Intent), PriorActionCount: session.Count + len(pending),
			Labels: session.Labels, LedgerHead: session.Head,
		},
		Identity: receiptIdentity(cfg, in.Run, rt.AgentSpec.Name),
		Decision: decision,
	}
}

// RecordActions signs one outcome receipt per completion and advances the session.
func (rt *Runtime) RecordActions(ctx context.Context, in ActionRecordInput) (ActionRecordOutput, error) {
	cfg := rt.AgentConfig.ActionPolicy.Resolved()
	byID := make(map[string]ActionVerdict, len(in.Verdicts))
	for _, verdict := range in.Verdicts {
		byID[verdict.ToolCallID] = verdict
	}
	state := in.Session
	window := cfg.ContextWindow
	var denied, deferred int
	for _, done := range in.Completed {
		verdict, ok := byID[done.ToolCallID]
		if !ok {
			return ActionRecordOutput{}, fmt.Errorf("%w: missing decision for %s", types.ErrActionPolicyUnavailable, done.ToolCallID)
		}
		labels := actionpolicy.UnionLabels(verdict.Labels, done.ResultLabels)
		labels = actionpolicy.UnionLabels(labels, done.ChildLabels)
		var err error
		var entry actionpolicy.LedgerEntry
		state, entry, err = actionpolicy.AppendEntry(state, actionpolicy.LedgerEntry{
			ActionID: verdict.ToolCallID, Tool: verdict.Tool, Kind: verdict.Kind,
			ArgsDigest: verdict.ArgsDigest, Decision: verdict.Result, Outcome: done.Status,
			Labels: labels, At: in.Now,
		}, window)
		if err != nil {
			return ActionRecordOutput{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
		}
		if verdict.IssuedDefer {
			state.Deferrals++
			deferred++
		}
		if verdict.Result == actionpolicy.ResultDeny {
			denied++
		}
		body := rt.outcomeReceipt(cfg, in, verdict, done, entry)
		signed, err := actionpolicy.Sign(ctx, cfg.Signer, actionpolicy.ReceiptID(in.RunID, verdict.ToolCallID, actionpolicy.ReceiptTypeOutcome), actionpolicy.ReceiptTypeOutcome, body)
		if err != nil {
			return ActionRecordOutput{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
		}
		if err := cfg.Sink.Write(ctx, signed); err != nil {
			return ActionRecordOutput{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
		}
	}
	loaded := in.LoadedHead
	if cfg.SessionStore != nil && in.Run.ConversationID != "" {
		if err := cfg.SessionStore.Save(ctx, in.Run.ConversationID, in.LoadedHead, &state); err != nil {
			if errors.Is(err, actionpolicy.ErrSessionConflict) {
				return ActionRecordOutput{}, err
			}
			return ActionRecordOutput{}, fmt.Errorf("%w: %w", types.ErrActionPolicyUnavailable, err)
		}
		loaded = state.Head
	}
	summary := SummaryFrom(state, in.RunID)
	summary.Denied = denied
	summary.Deferred = deferred
	return ActionRecordOutput{Session: state, LoadedHead: loaded, Summary: summary}, nil
}

func (rt *Runtime) outcomeReceipt(cfg actionpolicy.Config, in ActionRecordInput, verdict ActionVerdict, done ActionCompletion, entry actionpolicy.LedgerEntry) actionpolicy.Receipt {
	decision := actionpolicy.ReceiptDecision{
		Result: verdict.Result, PolicyID: cfg.Policy.ID(), PolicyVersion: cfg.Policy.Version(), PolicyHash: cfg.Policy.Hash(),
		Rule: verdict.Rule, Reason: verdict.Reason,
	}
	if verdict.Evaluated != "" && verdict.Evaluated != verdict.Result {
		decision.EvaluatedResult = verdict.Evaluated
	}
	return actionpolicy.Receipt{
		Action: actionpolicy.ReceiptAction{
			ID: verdict.ToolCallID, Tool: entry.Tool, ArgsDigest: verdict.ArgsDigest, Timestamp: in.Now,
		},
		Context: actionpolicy.ReceiptContext{
			SessionID: in.Session.ID, ConversationID: in.Run.ConversationID,
			IntentDigest: actionpolicy.DigestText(in.Run.Intent), PriorActionCount: in.Session.Count,
			Labels: in.Session.Labels, LedgerHead: in.Session.Head,
		},
		Identity: receiptIdentity(cfg, in.Run, rt.AgentSpec.Name),
		Decision: decision,
		Approval: done.Approval,
		Deferral: done.Deferral,
		Outcome: &actionpolicy.ReceiptOutcome{
			Status: done.Status, Cause: done.Cause, ExecutedArgsDigest: done.ExecutedArgsDigest,
			ResultDigest: done.ResultDigest, DeliveredDigest: done.DeliveredDigest,
			Error: done.Error, RecheckResult: done.Recheck,
		},
		Child:  done.Child,
		Ledger: &entry,
		Prev:   verdict.ReceiptHash,
	}
}

func (rt *Runtime) noteDecision(ctx context.Context, verdict ActionVerdict) {
	attrs := []interfaces.Attribute{
		{Key: types.MetricAttrActionResult, Value: string(verdict.Result)},
		{Key: types.MetricAttrTool, Value: verdict.Tool},
		{Key: types.MetricAttrActionRule, Value: verdict.Rule},
	}
	rt.Metrics.IncrementCounter(ctx, types.MetricActionPolicyDecision, attrs...)
	if verdict.IssuedDefer {
		rt.Metrics.IncrementCounter(ctx, types.MetricActionPolicyDeferral, attrs...)
	}
}

// DecisionEvent is the observability event for one verdict.
func DecisionEvent(verdict ActionVerdict) events.AgentEvent {
	return events.NewAgentCustomEvent(string(events.AgentCustomEventNameActionDecision), events.AgentCustomEventActionDecisionValue{
		ToolCallID: verdict.ToolCallID,
		Result:     string(verdict.Result),
		Rule:       verdict.Rule,
		Reason:     verdict.Reason,
		ReceiptID:  verdict.ReceiptID,
	})
}

// ActionToolMessage is the tool-role text for a call that did not run because of policy.
// Causes that already have an SDK message keep that message.
func ActionToolMessage(cause, reason, note, toolName string) string {
	switch cause {
	case actionpolicy.CauseUnknownTool:
		return UnknownToolMessage(toolName)
	case actionpolicy.CauseCircuitOpen:
		return CircuitBreakerSkippedMessage(toolName)
	case actionpolicy.CauseDeferralRejected:
		if note != "" {
			return "Action deferral was rejected: " + note
		}
		return "Action deferral was rejected."
	case actionpolicy.CauseDeferralTimeout:
		return "Action deferral timed out; continuing without running the tool."
	case actionpolicy.CauseDeferLimit:
		return "Action denied by policy: deferral limit exceeded"
	case actionpolicy.CauseRecheckDenied:
		if reason != "" {
			return "Action denied by policy on recheck: " + reason
		}
		return "Action denied by policy on recheck"
	case actionpolicy.CausePolicyDenied:
		if reason != "" {
			return "Action denied by policy: " + reason
		}
		return "Action denied by policy"
	default:
		if reason != "" {
			return "Action denied by policy: " + reason
		}
		return "Action denied by policy"
	}
}

// HumanDeferralApproved reports whether a resolved deferral skips the later approval pause.
func HumanDeferralApproved(res types.ActionResolution) bool {
	return res.Status == types.ApprovalStatusApproved && res.Method == actionpolicy.MethodHuman
}

// SummaryFrom builds the run summary from the session.
func SummaryFrom(state actionpolicy.SessionState, runID string) types.ActionSummary {
	sessionID := state.ID
	if sessionID == "" {
		sessionID = runID
	}
	summary := types.ActionSummary{SessionID: sessionID, LedgerHead: state.Head, Count: state.Count, Labels: state.Labels}
	return summary
}

func pendingOutcome(result actionpolicy.Result) string {
	if result == actionpolicy.ResultDeny {
		return actionpolicy.OutcomeNotExecuted
	}
	return actionpolicy.OutcomePending
}

// ActionObservation records what executeTool saw after the decision.
// Recheck is called only when a before-hook changed the arguments.
type ActionObservation struct {
	GatedDigest        string
	Recheck            func(context.Context, map[string]any) (actionpolicy.Result, string, error)
	Status             string
	Cause              string
	ResultDigest       string
	DeliveredDigest    string
	ExecutedArgsDigest string
	ResultLabels       []string
	RecheckResult      actionpolicy.Result
}

// noteRecheck denies execution when a before-hook changed the arguments and the
// policy does not allow the new arguments. It does not sign another decision.
func noteRecheck(ctx context.Context, obs *ActionObservation, tool string, args map[string]any) (string, bool, error) {
	if obs == nil || obs.Recheck == nil {
		return "", false, nil
	}
	digest, err := actionpolicy.DigestJSON(args)
	if err != nil || digest == obs.GatedDigest {
		return "", false, nil
	}
	result, reason, err := obs.Recheck(ctx, args)
	if err != nil {
		return "", false, err
	}
	if result == actionpolicy.ResultAllow {
		return "", false, nil
	}
	msg := ActionToolMessage(actionpolicy.CauseRecheckDenied, reason, "", tool)
	obs.Status = actionpolicy.OutcomeNotExecuted
	obs.Cause = actionpolicy.CauseRecheckDenied
	obs.RecheckResult = result
	obs.ResultDigest = actionpolicy.DigestText(msg)
	return msg, true, nil
}

// RecheckAction evaluates arguments a before-hook changed. It does not sign a receipt.
// Anything other than ALLOW is returned as-is so the caller can refuse the call.
func (rt *Runtime) RecheckAction(ctx context.Context, in ActionGateInput, session actionpolicy.SessionState, call ActionCall, args map[string]any) (actionpolicy.Result, string, error) {
	cfg := rt.AgentConfig.ActionPolicy.Resolved()
	dec, err := rt.evaluatePolicy(ctx, cfg, in, session, nil, call, nil, true, args)
	if err != nil {
		return "", "", err
	}
	return dec.Result, dec.Reason, nil
}

// noteActionResult records the tool's own result, then a delivered digest when a
// later hook changes that text. Labels are the union.
func noteActionResult(ctx context.Context, rt *Runtime, obs *ActionObservation, action actionpolicy.Action, raw, delivered string, args map[string]any) {
	if obs == nil || obs.Status != "" {
		return
	}
	obs.ResultDigest = actionpolicy.DigestText(raw)
	if digest, err := actionpolicy.DigestJSON(args); err == nil {
		obs.ExecutedArgsDigest = digest
	}
	obs.ResultLabels = classifyResultLabels(ctx, rt, action, raw)
	if delivered != raw {
		obs.DeliveredDigest = actionpolicy.DigestText(delivered)
		obs.ResultLabels = actionpolicy.UnionLabels(obs.ResultLabels, classifyResultLabels(ctx, rt, action, delivered))
	}
}

func classifyResultLabels(ctx context.Context, rt *Runtime, action actionpolicy.Action, text string) []string {
	if rt.AgentConfig.ActionPolicy == nil || rt.AgentConfig.ActionPolicy.Classifier == nil {
		return nil
	}
	labels, err := rt.AgentConfig.ActionPolicy.Classifier.ClassifyResult(ctx, action, text)
	if err != nil {
		if len(labels) > 0 {
			return labels
		}
		return []string{"restricted"}
	}
	return labels
}

func sessionView(in ActionGateInput, session actionpolicy.SessionState, pending []actionpolicy.PriorAction) actionpolicy.Session {
	actions := session.PriorActions()
	actions = append(actions, pending...)
	return actionpolicy.Session{
		ID: session.ID, ConversationID: in.Run.ConversationID, Intent: in.Run.Intent, Task: in.Run.Task,
		Actions: actions, ActionCount: session.Count + len(pending), Labels: session.Labels,
		Delegation: in.Run.Delegation, LedgerHead: session.Head,
	}
}

func agentIdentity(cfg actionpolicy.Config, in ActionGateInput, rt *Runtime) actionpolicy.Identity {
	return actionpolicy.Identity{
		Agent:     actionpolicy.AgentIdentity{ID: cfg.AgentID, Name: rt.AgentSpec.Name, KeyID: cfg.Signer.KeyID()},
		Requester: in.Run.Requester,
	}
}

func receiptIdentity(cfg actionpolicy.Config, run ActionRunContext, agentName string) actionpolicy.ReceiptIdentity {
	return actionpolicy.ReceiptIdentity{
		AgentID: cfg.AgentID, AgentName: agentName, KeyID: cfg.Signer.KeyID(),
		Principal: run.Requester.Principal, Service: run.Requester.Service,
		Roles: run.Requester.Roles, Scopes: run.Requester.Scopes, Verified: run.Requester.Verified,
		Delegation: run.Delegation,
	}
}
