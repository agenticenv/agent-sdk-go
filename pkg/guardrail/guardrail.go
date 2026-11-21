// Package guardrail checks user input once at the start of a run and model output
// after each LLM response. A deny ends the run. A modify replaces the text the
// rest of the run sees.
package guardrail

import "context"

// Stage is where a guardrail runs.
type Stage string

const (
	// StageInput runs once on the user prompt, before history, memory, and retrieval.
	StageInput Stage = "input"
	// StageOutput runs on each LLM response after AfterLLM hooks.
	StageOutput Stage = "output"
)

// Action is the decision a guardrail returns.
type Action string

const (
	ActionAllow  Action = "allow"
	ActionDeny   Action = "deny"
	ActionModify Action = "modify"
)

// Input is the text and run context for one evaluation.
type Input struct {
	Stage     Stage
	Text      string
	Final     bool // output only: the response has no tool calls
	AgentName string
	RunID     string
	Iteration int
}

// Decision is the result of one evaluation.
// Text is the replacement when Action is ActionModify. It may be empty.
type Decision struct {
	Action Action
	Reason string
	Text   string
}

// Allow continues the chain with the current text.
func Allow() Decision { return Decision{Action: ActionAllow} }

// Deny stops the chain and ends the run.
func Deny(reason string) Decision { return Decision{Action: ActionDeny, Reason: reason} }

// Modify replaces the text the next guardrail, and the rest of the run, sees.
func Modify(text, reason string) Decision {
	return Decision{Action: ActionModify, Text: text, Reason: reason}
}

// Guardrail evaluates text at one or more stages.
// Implementations must be safe for concurrent use.
type Guardrail interface {
	Name() string
	Stages() []Stage
	Evaluate(ctx context.Context, in Input) (Decision, error)
}

// Func is the body of a custom guardrail.
type Func func(ctx context.Context, in Input) (Decision, error)

// New builds a guardrail from a function. A nil fn fails closed when Evaluate runs.
// stages may be empty; the agent rejects a guardrail with no stages at build time.
func New(name string, fn Func, stages ...Stage) Guardrail {
	return &funcGuardrail{name: name, fn: fn, stages: copyStages(stages)}
}

type funcGuardrail struct {
	name   string
	fn     Func
	stages []Stage
}

func (g *funcGuardrail) Name() string { return g.name }

func (g *funcGuardrail) Stages() []Stage { return copyStages(g.stages) }

func (g *funcGuardrail) Evaluate(ctx context.Context, in Input) (Decision, error) {
	if g.fn == nil {
		return Decision{}, errNilEvaluate
	}
	return g.fn(ctx, in)
}

func copyStages(in []Stage) []Stage {
	if len(in) == 0 {
		return nil
	}
	out := make([]Stage, len(in))
	copy(out, in)
	return out
}
