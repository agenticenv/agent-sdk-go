package guardrail

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RegexDeny denies when pattern matches. pattern must be a valid non-empty regular expression.
func RegexDeny(name, pattern, reason string, stages ...Stage) (Guardrail, error) {
	re, err := compilePattern("RegexDeny", pattern)
	if err != nil {
		return nil, err
	}
	return &regexDeny{name: name, reason: reason, re: re, stages: copyStages(stages)}, nil
}

type regexDeny struct {
	name, reason string
	re           *regexp.Regexp
	stages       []Stage
}

func (g *regexDeny) Name() string    { return g.name }
func (g *regexDeny) Stages() []Stage { return copyStages(g.stages) }

func (g *regexDeny) Evaluate(_ context.Context, in Input) (Decision, error) {
	if g.re.MatchString(in.Text) {
		return Deny(g.reason), nil
	}
	return Allow(), nil
}

// RegexRedact replaces every match. An empty replacement is valid and erases the match.
// When the pattern matches, the decision is modify with reason "redacted". Otherwise allow.
func RegexRedact(name, pattern, replacement string, stages ...Stage) (Guardrail, error) {
	re, err := compilePattern("RegexRedact", pattern)
	if err != nil {
		return nil, err
	}
	return &regexRedact{name: name, replacement: replacement, re: re, stages: copyStages(stages)}, nil
}

type regexRedact struct {
	name, replacement string
	re                *regexp.Regexp
	stages            []Stage
}

func (g *regexRedact) Name() string    { return g.name }
func (g *regexRedact) Stages() []Stage { return copyStages(g.stages) }

func (g *regexRedact) Evaluate(_ context.Context, in Input) (Decision, error) {
	if !g.re.MatchString(in.Text) {
		return Allow(), nil
	}
	return Modify(g.re.ReplaceAllString(in.Text, g.replacement), "redacted"), nil
}

// MaxLength denies when the text has more than maxRunes Unicode code points.
func MaxLength(name string, maxRunes int, stages ...Stage) (Guardrail, error) {
	if maxRunes <= 0 {
		return nil, fmt.Errorf("MaxLength: maxRunes must be > 0")
	}
	return &maxLength{name: name, maxRunes: maxRunes, stages: copyStages(stages)}, nil
}

type maxLength struct {
	name     string
	maxRunes int
	stages   []Stage
}

func (g *maxLength) Name() string    { return g.name }
func (g *maxLength) Stages() []Stage { return copyStages(g.stages) }

func (g *maxLength) Evaluate(_ context.Context, in Input) (Decision, error) {
	if utf8.RuneCountInString(in.Text) > g.maxRunes {
		return Deny(fmt.Sprintf("exceeds max length of %d runes", g.maxRunes)), nil
	}
	return Allow(), nil
}

// DenyList denies when the text contains any term, compared as a case-insensitive substring.
// Terms are checked in the order given. The first match denies. Terms are not trimmed;
// a term of " " is valid. An empty list or an empty term is an error.
func DenyList(name string, terms []string, stages ...Stage) (Guardrail, error) {
	if len(terms) == 0 {
		return nil, fmt.Errorf("DenyList: terms are required")
	}
	lowered := make([]string, len(terms))
	for i, term := range terms {
		if term == "" {
			return nil, fmt.Errorf("DenyList: term must not be empty")
		}
		lowered[i] = strings.ToLower(term)
	}
	return &denyList{name: name, terms: lowered, stages: copyStages(stages)}, nil
}

type denyList struct {
	name   string
	terms  []string
	stages []Stage
}

func (g *denyList) Name() string    { return g.name }
func (g *denyList) Stages() []Stage { return copyStages(g.stages) }

func (g *denyList) Evaluate(_ context.Context, in Input) (Decision, error) {
	text := strings.ToLower(in.Text)
	for _, term := range g.terms {
		if strings.Contains(text, term) {
			return Deny("contains denied term"), nil
		}
	}
	return Allow(), nil
}

// JSON allows non-final output and denies a final response that is not valid JSON
// after surrounding whitespace is removed.
func JSON(name string) Guardrail {
	return &jsonGuardrail{name: name}
}

type jsonGuardrail struct {
	name string
}

func (g *jsonGuardrail) Name() string    { return g.name }
func (g *jsonGuardrail) Stages() []Stage { return []Stage{StageOutput} }

func (g *jsonGuardrail) Evaluate(_ context.Context, in Input) (Decision, error) {
	if !in.Final {
		return Allow(), nil
	}
	if !json.Valid([]byte(strings.TrimSpace(in.Text))) {
		return Deny("invalid json"), nil
	}
	return Allow(), nil
}

func compilePattern(ctor, pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, fmt.Errorf("%s: pattern is required", ctor)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ctor, err)
	}
	return re, nil
}
