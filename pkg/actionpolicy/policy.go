// Package actionpolicy decides whether a tool call may run.
//
// Register a policy with agent.WithActionPolicy. Before a tool runs, the agent
// signs a decision. After the tool finishes, or when it does not run, the agent
// signs an outcome. Verify both offline with [VerifyReceipt], [VerifyAction], and
// [VerifyLedger]. An agent without a policy keeps the existing tool path.
package actionpolicy

import "context"

// Result is the policy decision for one tool call.
type Result string

const (
	ResultAllow  Result = "ALLOW"
	ResultDeny   Result = "DENY"
	ResultModify Result = "MODIFY"
	ResultStepUp Result = "STEP_UP"
	ResultDefer  Result = "DEFER"
)

// Known reports whether r is one of the five decisions.
func (r Result) Known() bool {
	switch r {
	case ResultAllow, ResultDeny, ResultModify, ResultStepUp, ResultDefer:
		return true
	default:
		return false
	}
}

// Policy evaluates one tool call against the current session and identity.
// ID, Version, and Hash identify the policy in receipts and in the agent fingerprint.
// Hash is a digest of the policy content. Change Version when matcher code changes,
// because functions are not part of Hash.
type Policy interface {
	ID() string
	Version() string
	Hash() string
	Evaluate(ctx context.Context, req Request) (Decision, error)
}

// Request is one evaluation. Session includes prior actions and the labels
// accumulated so far in this batch.
type Request struct {
	Action   Action
	Session  Session
	Identity Identity
}

// Action is the tool call being evaluated.
type Action struct {
	ID        string
	Tool      string
	Kind      string
	Args      map[string]any
	Labels    []string
	RunID     string
	Iteration int
	// Recheck is true when the call is evaluated again because a hook changed the arguments.
	Recheck bool
}

// Session is the context the policy sees for this call.
type Session struct {
	ID             string
	ConversationID string
	// Intent is the root user prompt. Delegated runs keep the parent's intent.
	Intent string
	// Task is this run's prompt. A delegated run uses the delegated query.
	Task        string
	Actions     []PriorAction
	ActionCount int
	Labels      []string
	Delegation  []Hop
	LedgerHead  string
}

// PriorAction is one earlier tool call in this session, oldest first.
// Outcome is "pending" for an earlier call in the current batch that has not finished,
// and "not_executed" when that call was already denied.
type PriorAction struct {
	Seq        int      `json:"seq"`
	Tool       string   `json:"tool"`
	Kind       string   `json:"kind"`
	ArgsDigest string   `json:"argsDigest"`
	Decision   Result   `json:"decision"`
	Outcome    string   `json:"outcome"`
	Labels     []string `json:"labels,omitempty"`
}

// Hop is one delegation step, root first.
type Hop struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	RunID     string `json:"runId"`
	ActionID  string `json:"actionId"`
}

// Identity is the agent that signed the decision and the caller who started the run.
type Identity struct {
	Agent     AgentIdentity
	Requester Requester
}

// AgentIdentity is the stable identity bound to the signing key.
type AgentIdentity struct {
	ID    string
	Name  string
	KeyID string
}

// Decision is the result of one evaluation.
// Args is set only for [ResultModify] and replaces the arguments used for
// authorization and execution. The assistant message keeps the model's arguments.
type Decision struct {
	Result Result
	Reason string
	Rule   string
	Args   map[string]any
}

// Allow continues with the arguments the model produced.
func Allow() Decision { return Decision{Result: ResultAllow} }

// Deny refuses the call. The run continues and the model sees the reason.
func Deny(reason string) Decision { return Decision{Result: ResultDeny, Reason: reason} }

// Modify replaces the arguments used for authorization and execution.
func Modify(args map[string]any, reason string) Decision {
	return Decision{Result: ResultModify, Reason: reason, Args: cloneArgs(args)}
}

// StepUp pauses for approval before the tool runs.
func StepUp(reason string) Decision { return Decision{Result: ResultStepUp, Reason: reason} }

// Defer pauses until a resolver approves or rejects the call.
func Defer(reason string) Decision { return Decision{Result: ResultDefer, Reason: reason} }

func cloneArgs(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
