package base

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/guardrail"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

const guardrailActionError = "error"

// ApplyInputGuardrailsInput is the user prompt and the ids written on the evaluation record.
type ApplyInputGuardrailsInput struct {
	Logger    logger.Logger
	AgentName string
	RunID     string
	Text      string
}

// HasGuardrails reports whether any configured guardrail declares stage.
func (rt *Runtime) HasGuardrails(stage guardrail.Stage) bool {
	for _, g := range rt.AgentConfig.Guardrails {
		if guardrailDeclares(g, stage) {
			return true
		}
	}
	return false
}

// ApplyInputGuardrails runs the input stage once. Iteration on the record is 0.
// The returned text replaces the user prompt. A deny returns *guardrail.Error.
func (rt *Runtime) ApplyInputGuardrails(ctx context.Context, in ApplyInputGuardrailsInput) (string, error) {
	return rt.evaluateGuardrails(ctx, in.Logger, guardrail.Input{
		Stage:     guardrail.StageInput,
		Text:      in.Text,
		AgentName: in.AgentName,
		RunID:     in.RunID,
		Iteration: 0,
	})
}

// evaluateGuardrails runs guardrails that declare in.Stage, in registration order.
// A modify is the text the next guardrail sees. The first deny stops the chain.
// An evaluate error, an empty action, or an unknown action is an ordinary step error.
func (rt *Runtime) evaluateGuardrails(ctx context.Context, log logger.Logger, in guardrail.Input) (string, error) {
	text := in.Text
	for _, g := range rt.AgentConfig.Guardrails {
		if !guardrailDeclares(g, in.Stage) {
			continue
		}
		evalIn := in
		evalIn.Text = text
		dec, err := g.Evaluate(ctx, evalIn)
		if err != nil {
			rt.recordGuardrailDecision(ctx, log, guardrailDecision{
				Guardrail: g.Name(),
				Stage:     in.Stage,
				Action:    guardrailActionError,
				Reason:    err.Error(),
				AgentName: in.AgentName,
				RunID:     in.RunID,
				Iteration: in.Iteration,
			})
			return "", fmt.Errorf("agent: guardrail %q (%s): evaluate: %w", g.Name(), in.Stage, err)
		}
		switch dec.Action {
		case guardrail.ActionAllow:
			rt.recordGuardrailDecision(ctx, log, decisionFrom(g.Name(), in, string(dec.Action), dec.Reason))
		case guardrail.ActionModify:
			text = dec.Text
			rt.recordGuardrailDecision(ctx, log, decisionFrom(g.Name(), in, string(dec.Action), dec.Reason))
		case guardrail.ActionDeny:
			rt.recordGuardrailDecision(ctx, log, decisionFrom(g.Name(), in, string(dec.Action), dec.Reason))
			return "", &guardrail.Error{Guardrail: g.Name(), Stage: in.Stage, Reason: dec.Reason}
		case "":
			rt.recordGuardrailDecision(ctx, log, decisionFrom(g.Name(), in, guardrailActionError, dec.Reason))
			return "", fmt.Errorf("agent: guardrail %q (%s): empty action", g.Name(), in.Stage)
		default:
			rt.recordGuardrailDecision(ctx, log, decisionFrom(g.Name(), in, guardrailActionError, dec.Reason))
			return "", fmt.Errorf("agent: guardrail %q (%s): unknown action %q", g.Name(), in.Stage, dec.Action)
		}
	}
	return text, nil
}

type guardrailDecision struct {
	Guardrail string
	Stage     guardrail.Stage
	Action    string
	Reason    string
	AgentName string
	RunID     string
	Iteration int
}

func decisionFrom(name string, in guardrail.Input, action, reason string) guardrailDecision {
	return guardrailDecision{
		Guardrail: name,
		Stage:     in.Stage,
		Action:    action,
		Reason:    reason,
		AgentName: in.AgentName,
		RunID:     in.RunID,
		Iteration: in.Iteration,
	}
}

// recordGuardrailDecision emits one span, one counter, and one info log.
// The checked text is not recorded.
func (rt *Runtime) recordGuardrailDecision(ctx context.Context, log logger.Logger, d guardrailDecision) {
	attrs := []interfaces.Attribute{
		{Key: types.MetricAttrGuardrail, Value: d.Guardrail},
		{Key: types.MetricAttrGuardrailStage, Value: string(d.Stage)},
		{Key: types.MetricAttrGuardrailAction, Value: d.Action},
	}
	if rt.Tracer != nil {
		var sp interfaces.Span
		ctx, sp = rt.Tracer.StartSpan(ctx, "guardrail.evaluate", attrs...)
		if d.Action == guardrailActionError {
			sp.RecordError(errors.New(d.Reason))
		}
		sp.End()
	}
	if rt.Metrics != nil {
		rt.Metrics.IncrementCounter(ctx, types.MetricGuardrailEvaluated, attrs...)
	}
	if log != nil {
		log.Info(ctx, "runtime: guardrail evaluated",
			slog.String("scope", "runtime"),
			slog.String("guardrail", d.Guardrail),
			slog.String("guardrail.stage", string(d.Stage)),
			slog.String("guardrail.action", d.Action),
			slog.String("reason", d.Reason),
			slog.String("agent.name", d.AgentName),
			slog.String("run_id", d.RunID),
			slog.Int("iteration", d.Iteration),
		)
	}
}

func guardrailDeclares(g guardrail.Guardrail, stage guardrail.Stage) bool {
	if g == nil {
		return false
	}
	for _, s := range g.Stages() {
		if s == stage {
			return true
		}
	}
	return false
}
