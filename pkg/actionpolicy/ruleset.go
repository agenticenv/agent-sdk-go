package actionpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Class is the evaluation group of a rule.
type Class int

const (
	// ClassForbidden is checked first. A match is always [ResultDeny], in declaration order.
	ClassForbidden Class = iota + 1
	// ClassContextual is a rule that depends on session labels, prior calls, or identity.
	ClassContextual
	// ClassStandard is a rule that does not depend on session context.
	ClassStandard
)

func (c Class) String() string {
	switch c {
	case ClassForbidden:
		return "forbidden"
	case ClassContextual:
		return "contextual"
	case ClassStandard:
		return "standard"
	default:
		return ""
	}
}

// SDK rule ids written by [NewRuleSet] when it synthesizes a decision.
const (
	RulePolicyError    = "sdk:policy_error"
	RulePolicyConflict = "sdk:policy_conflict"
	RuleContextMissing = "sdk:context_missing"
	RuleUnknownTool    = "sdk:unknown_tool"
)

// Matcher reports whether a rule applies to req.
// Return [ErrContextMissing] when a required value is absent. Any other error denies the call.
type Matcher func(req Request) (bool, error)

// Rule is one entry in a [NewRuleSet] policy.
// Priority applies to contextual and standard rules. Higher priority wins.
// Forbidden rules ignore Priority and [Decision.Result].
type Rule struct {
	ID       string
	Class    Class
	Priority int
	Match    Matcher
	Decision Decision
	// Modify builds replacement arguments when this rule wins with [ResultModify].
	// It takes precedence over [Decision.Args]. A nil return denies the call.
	Modify func(Request) map[string]any
}

type ruleSet struct {
	id      string
	version string
	def     Result
	rules   []Rule
	hash    string
}

// NewRuleSet returns a policy that evaluates rules in class order.
// def is the decision when no rule matches. It must be ALLOW, DENY, STEP_UP, or DEFER.
//
// Forbidden rules are checked first. The first match denies the call and supplies
// the rule id and reason. Among the remaining rules, the highest priority match wins.
// Equal priority with the same result uses the earlier rule. Equal priority with
// different results returns DEFER with rule sdk:policy_conflict.
// A matcher that returns [ErrContextMissing] returns DEFER. Any other matcher error
// returns DENY with rule sdk:policy_error.
func NewRuleSet(id, version string, def Result, rules ...Rule) (Policy, error) {
	if id == "" || version == "" {
		return nil, fmt.Errorf("actionpolicy: rule set id and version are required")
	}
	switch def {
	case ResultAllow, ResultDeny, ResultStepUp, ResultDefer:
	default:
		return nil, fmt.Errorf("actionpolicy: rule set default must be ALLOW, DENY, STEP_UP, or DEFER")
	}
	seen := make(map[string]struct{}, len(rules))
	copied := make([]Rule, len(rules))
	for i, rule := range rules {
		if rule.ID == "" {
			return nil, fmt.Errorf("actionpolicy: rule id is required")
		}
		if _, dup := seen[rule.ID]; dup {
			return nil, fmt.Errorf("actionpolicy: duplicate rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		if rule.Class.String() == "" {
			return nil, fmt.Errorf("actionpolicy: rule %q has an invalid class", rule.ID)
		}
		if rule.Match == nil {
			return nil, fmt.Errorf("actionpolicy: rule %q has no matcher", rule.ID)
		}
		if !rule.Decision.Result.Known() {
			return nil, fmt.Errorf("actionpolicy: rule %q has an invalid result", rule.ID)
		}
		rule.Decision.Args = cloneArgs(rule.Decision.Args)
		copied[i] = rule
	}
	rs := &ruleSet{id: id, version: version, def: def, rules: copied}
	sum, err := rs.computeHash()
	if err != nil {
		return nil, err
	}
	rs.hash = sum
	return rs, nil
}

func (rs *ruleSet) ID() string      { return rs.id }
func (rs *ruleSet) Version() string { return rs.version }
func (rs *ruleSet) Hash() string    { return rs.hash }

func (rs *ruleSet) computeHash() (string, error) {
	body := struct {
		ID      string     `json:"id"`
		Version string     `json:"version"`
		Default Result     `json:"default"`
		Rules   []hashRule `json:"rules"`
	}{ID: rs.id, Version: rs.version, Default: rs.def}
	body.Rules = make([]hashRule, len(rs.rules))
	for i, rule := range rs.rules {
		body.Rules[i] = hashRule{
			ID:       rule.ID,
			Class:    rule.Class.String(),
			Priority: rule.Priority,
			Result:   rule.Decision.Result,
		}
	}
	return DigestJSON(body)
}

type hashRule struct {
	ID       string `json:"id"`
	Class    string `json:"class"`
	Priority int    `json:"priority"`
	Result   Result `json:"result"`
}

func (rs *ruleSet) Evaluate(_ context.Context, req Request) (Decision, error) {
	for _, rule := range rs.rules {
		if rule.Class != ClassForbidden {
			continue
		}
		dec, done, err := matchRule(rule, req)
		if err != nil || done {
			return dec, err
		}
	}

	var winners []Rule
	for _, rule := range rs.rules {
		if rule.Class == ClassForbidden {
			continue
		}
		ok, err := rule.Match(req)
		if err != nil {
			return decisionForMatchError(err), nil
		}
		if !ok {
			continue
		}
		if len(winners) == 0 || rule.Priority > winners[0].Priority {
			winners = []Rule{rule}
			continue
		}
		if rule.Priority == winners[0].Priority {
			winners = append(winners, rule)
		}
	}
	if len(winners) == 0 {
		return Decision{Result: rs.def}, nil
	}
	first := winners[0].Decision.Result
	for _, rule := range winners[1:] {
		if rule.Decision.Result != first {
			return Decision{Result: ResultDefer, Rule: RulePolicyConflict, Reason: "matching rules disagree"}, nil
		}
	}
	return materialize(winners[0], req), nil
}

func matchRule(rule Rule, req Request) (Decision, bool, error) {
	ok, err := rule.Match(req)
	if err != nil {
		return decisionForMatchError(err), true, nil
	}
	if !ok {
		return Decision{}, false, nil
	}
	return materialize(rule, req), true, nil
}

func decisionForMatchError(err error) Decision {
	if errors.Is(err, ErrContextMissing) {
		return Decision{Result: ResultDefer, Rule: RuleContextMissing, Reason: "context missing"}
	}
	reason := "policy error"
	if err != nil && err.Error() != "" {
		reason = err.Error()
	}
	return Decision{Result: ResultDeny, Rule: RulePolicyError, Reason: reason}
}

func materialize(rule Rule, req Request) Decision {
	if rule.Class == ClassForbidden {
		return Decision{Result: ResultDeny, Rule: rule.ID, Reason: rule.Decision.Reason}
	}
	dec := rule.Decision
	dec.Rule = rule.ID
	dec.Args = cloneArgs(rule.Decision.Args)
	if dec.Result != ResultModify {
		dec.Args = nil
		return dec
	}
	if rule.Modify != nil {
		dec.Args = cloneArgs(rule.Modify(req))
	}
	if dec.Args == nil {
		return Decision{Result: ResultDeny, Rule: RulePolicyError, Reason: "policy modify returned no arguments"}
	}
	return dec
}

// Tool matches when the call's tool name is one of names.
func Tool(names ...string) Matcher {
	set := copySet(names)
	return func(req Request) (bool, error) {
		_, ok := set[req.Action.Tool]
		return ok, nil
	}
}

// Kind matches when the call's kind is one of kinds.
func Kind(kinds ...string) Matcher {
	set := copySet(kinds)
	return func(req Request) (bool, error) {
		_, ok := set[req.Action.Kind]
		return ok, nil
	}
}

// HasLabel matches when the call itself carries label.
func HasLabel(label string) Matcher {
	return func(req Request) (bool, error) {
		return containsString(req.Action.Labels, label), nil
	}
}

// SessionHasLabel matches when the session has accumulated label.
func SessionHasLabel(label string) Matcher {
	return func(req Request) (bool, error) {
		return containsString(req.Session.Labels, label), nil
	}
}

// PriorTool matches when an earlier call invoked name.
func PriorTool(name string) Matcher {
	return func(req Request) (bool, error) {
		for _, prior := range req.Session.Actions {
			if prior.Tool == name {
				return true, nil
			}
		}
		return false, nil
	}
}

// PriorPending matches when an earlier call in this batch has not finished.
func PriorPending() Matcher {
	return func(req Request) (bool, error) {
		for _, prior := range req.Session.Actions {
			if prior.Outcome == OutcomePending {
				return true, nil
			}
		}
		return false, nil
	}
}

// RequesterHasRole matches when the caller has role.
func RequesterHasRole(role string) Matcher {
	return func(req Request) (bool, error) {
		return containsString(req.Identity.Requester.Roles, role), nil
	}
}

// Unverified matches when the caller was not verified.
func Unverified() Matcher {
	return func(req Request) (bool, error) {
		return !req.Identity.Requester.Verified, nil
	}
}

// And matches when every matcher matches.
// It stops on the first false result or the first error.
func And(matchers ...Matcher) Matcher {
	return func(req Request) (bool, error) {
		for _, match := range matchers {
			if match == nil {
				return false, fmt.Errorf("actionpolicy: nil matcher")
			}
			ok, err := match(req)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	}
}

// Or matches when any matcher matches.
// It stops on the first true result or the first error.
func Or(matchers ...Matcher) Matcher {
	return func(req Request) (bool, error) {
		for _, match := range matchers {
			if match == nil {
				return false, fmt.Errorf("actionpolicy: nil matcher")
			}
			ok, err := match(req)
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
}

// Not inverts a successful match. It returns a matcher error unchanged.
func Not(match Matcher) Matcher {
	return func(req Request) (bool, error) {
		if match == nil {
			return false, fmt.Errorf("actionpolicy: nil matcher")
		}
		ok, err := match(req)
		if err != nil {
			return false, err
		}
		return !ok, nil
	}
}

// ArgKind is the JSON kind checked by [ArgType].
type ArgKind string

const (
	ArgKindString ArgKind = "string"
	ArgKindNumber ArgKind = "number"
	ArgKindBool   ArgKind = "bool"
	ArgKindArray  ArgKind = "array"
	ArgKindObject ArgKind = "object"
)

// ArgType matches when the value at path has kind.
// A missing path returns [ErrContextMissing]. A present value of another kind does not match.
// path is dot-separated fields of nested map[string]any values.
func ArgType(path string, kind ArgKind) Matcher {
	return func(req Request) (bool, error) {
		value, err := lookupArg(req.Action.Args, path)
		if err != nil {
			return false, err
		}
		got, ok := kindOf(value)
		if !ok || got != kind {
			return false, nil
		}
		return true, nil
	}
}

// ArgRange matches when the number at path is inside the inclusive interval [min, max].
// A missing path returns [ErrContextMissing]. A non-numeric value does not match.
func ArgRange(path string, min, max float64) Matcher {
	return func(req Request) (bool, error) {
		value, err := lookupArg(req.Action.Args, path)
		if err != nil {
			return false, err
		}
		number, ok := asFloat(value)
		if !ok {
			return false, nil
		}
		return number >= min && number <= max, nil
	}
}

// ArgPattern matches when the string at path matches re.
// A missing path returns [ErrContextMissing]. A non-string value does not match.
func ArgPattern(path string, re *regexp.Regexp) Matcher {
	return func(req Request) (bool, error) {
		if re == nil {
			return false, fmt.Errorf("actionpolicy: nil pattern")
		}
		value, err := lookupArg(req.Action.Args, path)
		if err != nil {
			return false, err
		}
		text, ok := value.(string)
		if !ok {
			return false, nil
		}
		return re.MatchString(text), nil
	}
}

// ArgIn matches when the value at path equals one of values.
// Numbers compare numerically. A missing path returns [ErrContextMissing].
func ArgIn(path string, values ...any) Matcher {
	return func(req Request) (bool, error) {
		value, err := lookupArg(req.Action.Args, path)
		if err != nil {
			return false, err
		}
		for _, candidate := range values {
			if argEqual(value, candidate) {
				return true, nil
			}
		}
		return false, nil
	}
}

// ArgNotIn matches when the value at path equals none of values.
// A missing path returns [ErrContextMissing].
func ArgNotIn(path string, values ...any) Matcher {
	inner := ArgIn(path, values...)
	return func(req Request) (bool, error) {
		ok, err := inner(req)
		if err != nil {
			return false, err
		}
		return !ok, nil
	}
}

func lookupArg(args map[string]any, path string) (any, error) {
	if path == "" {
		return nil, ErrContextMissing
	}
	var current any = args
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, ErrContextMissing
		}
		value, exists := object[part]
		if !exists || value == nil {
			return nil, ErrContextMissing
		}
		current = value
	}
	return current, nil
}

func kindOf(value any) (ArgKind, bool) {
	switch value.(type) {
	case string:
		return ArgKindString, true
	case bool:
		return ArgKindBool, true
	}
	if _, ok := asFloat(value); ok {
		return ArgKindNumber, true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map:
		return ArgKindObject, true
	case reflect.Slice, reflect.Array:
		return ArgKindArray, true
	default:
		return "", false
	}
}

func asFloat(value any) (float64, bool) {
	switch n := value.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func argEqual(left, right any) bool {
	if lf, ok := asFloat(left); ok {
		rf, rok := asFloat(right)
		return rok && lf == rf
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func copySet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
