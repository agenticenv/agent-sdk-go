package actionpolicy

import (
	"context"
	"errors"
	"regexp"
	"testing"
)

func TestRuleSetOrderAndConflict(t *testing.T) {
	policy, err := NewRuleSet("p", "1", ResultAllow,
		Rule{ID: "std-low", Class: ClassStandard, Priority: 10, Match: Tool("echo"), Decision: Allow()},
		Rule{ID: "forbid", Class: ClassForbidden, Priority: 100, Match: Tool("rm"), Decision: Decision{Result: ResultAllow, Reason: "never"}},
		Rule{ID: "ctx", Class: ClassContextual, Priority: 10, Match: HasLabel("restricted"), Decision: StepUp("needs review")},
		Rule{ID: "std-high", Class: ClassStandard, Priority: 10, Match: Tool("echo"), Decision: Deny("no echo")},
	)
	if err != nil {
		t.Fatal(err)
	}

	dec, err := policy.Evaluate(context.Background(), reqTool("rm", nil))
	if err != nil || dec.Result != ResultDeny || dec.Rule != "forbid" || dec.Reason != "never" {
		t.Fatalf("forbidden = %+v %v", dec, err)
	}
	dec, err = policy.Evaluate(context.Background(), reqTool("echo", nil))
	if err != nil || dec.Result != ResultDefer || dec.Rule != RulePolicyConflict {
		t.Fatalf("conflict = %+v %v", dec, err)
	}

	labeled := reqTool("other", nil)
	labeled.Action.Labels = []string{"restricted"}
	dec, err = policy.Evaluate(context.Background(), labeled)
	if err != nil || dec.Result != ResultStepUp || dec.Rule != "ctx" {
		t.Fatalf("priority = %+v %v", dec, err)
	}

	dec, err = policy.Evaluate(context.Background(), reqTool("other", nil))
	if err != nil || dec.Result != ResultAllow || dec.Rule != "" {
		t.Fatalf("default = %+v %v", dec, err)
	}
}

func TestRuleSetSamePriorityKeepsFirst(t *testing.T) {
	policy, err := NewRuleSet("p", "1", ResultDeny,
		Rule{ID: "first", Class: ClassStandard, Priority: 5, Match: Tool("echo"), Decision: Deny("first")},
		Rule{ID: "second", Class: ClassStandard, Priority: 5, Match: Tool("echo"), Decision: Deny("second")},
	)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := policy.Evaluate(context.Background(), reqTool("echo", nil))
	if err != nil || dec.Rule != "first" || dec.Reason != "first" {
		t.Fatalf("first = %+v %v", dec, err)
	}
}

func TestRuleSetContextMissingAndMatcherError(t *testing.T) {
	policy, err := NewRuleSet("p", "1", ResultAllow,
		Rule{ID: "need", Class: ClassContextual, Priority: 1, Match: func(Request) (bool, error) {
			return false, ErrContextMissing
		}, Decision: Deny("unused")},
		Rule{ID: "bad", Class: ClassStandard, Priority: 1, Match: func(Request) (bool, error) {
			return false, errors.New("boom")
		}, Decision: Allow()},
	)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := policy.Evaluate(context.Background(), reqTool("echo", nil))
	if err != nil || dec.Result != ResultDefer || dec.Rule != RuleContextMissing {
		t.Fatalf("missing = %+v %v", dec, err)
	}

	policy, err = NewRuleSet("p", "1", ResultAllow,
		Rule{ID: "bad", Class: ClassStandard, Priority: 1, Match: func(Request) (bool, error) {
			return false, errors.New("boom")
		}, Decision: Allow()},
	)
	if err != nil {
		t.Fatal(err)
	}
	dec, err = policy.Evaluate(context.Background(), reqTool("echo", nil))
	if err != nil || dec.Result != ResultDeny || dec.Rule != RulePolicyError {
		t.Fatalf("error = %+v %v", dec, err)
	}
}

func TestRuleSetModify(t *testing.T) {
	policy, err := NewRuleSet("p", "1", ResultAllow,
		Rule{
			ID: "mod", Class: ClassStandard, Priority: 1, Match: Tool("echo"),
			Decision: Modify(map[string]any{"from": "decision"}, "replaced"),
			Modify: func(Request) map[string]any {
				return map[string]any{"from": "func"}
			},
		},
		Rule{ID: "nil-mod", Class: ClassStandard, Priority: 1, Match: Tool("wipe"), Decision: Modify(nil, "nope")},
		Rule{ID: "allow", Class: ClassStandard, Priority: 1, Match: Tool("keep"), Decision: Decision{Result: ResultAllow, Args: map[string]any{"x": 1}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := policy.Evaluate(context.Background(), reqTool("echo", nil))
	if err != nil || dec.Result != ResultModify || dec.Args["from"] != "func" {
		t.Fatalf("modify func = %+v %v", dec, err)
	}
	dec, err = policy.Evaluate(context.Background(), reqTool("wipe", nil))
	if err != nil || dec.Result != ResultDeny || dec.Rule != RulePolicyError {
		t.Fatalf("nil modify = %+v %v", dec, err)
	}
	dec, err = policy.Evaluate(context.Background(), reqTool("keep", nil))
	if err != nil || dec.Result != ResultAllow || dec.Args != nil {
		t.Fatalf("allow dropped args = %+v %v", dec, err)
	}
}

func TestRuleSetValidatorsAndCombinators(t *testing.T) {
	re := regexp.MustCompile(`^ok-`)
	policy, err := NewRuleSet("p", "1", ResultDeny,
		Rule{ID: "ok", Class: ClassStandard, Priority: 1, Match: And(
			Tool("pay"),
			ArgType("amount", ArgKindNumber),
			ArgRange("amount", 1, 10),
			ArgPattern("note", re),
			ArgIn("currency", "usd"),
			ArgNotIn("currency", "eur"),
			Not(Unverified()),
		), Decision: Allow()},
	)
	if err != nil {
		t.Fatal(err)
	}
	call := reqTool("pay", map[string]any{
		"amount":   5,
		"note":     "ok-1",
		"currency": "usd",
		"user":     map[string]any{"id": "u"},
	})
	call.Identity.Requester.Verified = true
	dec, err := policy.Evaluate(context.Background(), call)
	if err != nil || dec.Result != ResultAllow || dec.Rule != "ok" {
		t.Fatalf("valid = %+v %v", dec, err)
	}

	call.Action.Args["amount"] = "five"
	dec, err = policy.Evaluate(context.Background(), call)
	if err != nil || dec.Result != ResultDeny || dec.Rule != "" {
		t.Fatalf("wrong type = %+v %v", dec, err)
	}
	delete(call.Action.Args, "note")
	call.Action.Args["amount"] = 5
	dec, err = policy.Evaluate(context.Background(), call)
	if err != nil || dec.Result != ResultDefer || dec.Rule != RuleContextMissing {
		t.Fatalf("missing path = %+v %v", dec, err)
	}

	short := And(func(Request) (bool, error) { return false, nil }, func(Request) (bool, error) {
		t.Fatal("And evaluated the second matcher")
		return true, nil
	})
	if ok, err := short(Request{}); ok || err != nil {
		t.Fatalf("and short = %v %v", ok, err)
	}
	missing := Not(func(Request) (bool, error) { return false, ErrContextMissing })
	if _, err := missing(Request{}); !errors.Is(err, ErrContextMissing) {
		t.Fatalf("not error = %v", err)
	}
}

func TestRuleSetHashIgnoresMatchers(t *testing.T) {
	left, err := NewRuleSet("p", "1", ResultDeny, Rule{ID: "a", Class: ClassStandard, Priority: 2, Match: Tool("a"), Decision: Deny("no")})
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewRuleSet("p", "1", ResultDeny, Rule{ID: "a", Class: ClassStandard, Priority: 2, Match: Tool("b"), Decision: Deny("no")})
	if err != nil {
		t.Fatal(err)
	}
	if left.Hash() == "" || left.Hash() != right.Hash() {
		t.Fatalf("hash changed with matcher: %s vs %s", left.Hash(), right.Hash())
	}
	changed, err := NewRuleSet("p", "2", ResultDeny, Rule{ID: "a", Class: ClassStandard, Priority: 2, Match: Tool("a"), Decision: Deny("no")})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Hash() == left.Hash() {
		t.Fatal("version did not change the hash")
	}
	if _, err := NewRuleSet("p", "1", ResultModify); err == nil {
		t.Fatal("expected MODIFY default to be rejected")
	}
	if _, err := NewRuleSet("", "1", ResultAllow); err == nil {
		t.Fatal("expected empty id to be rejected")
	}
}

func TestClassifierDefaultsAndPatterns(t *testing.T) {
	clf := NewClassifier("c", ClassifierConfig{
		Tools: map[string][]string{"echo": {"public"}},
		Patterns: map[string]*regexp.Regexp{
			"secret": regexp.MustCompile(`secret`),
		},
	}).(*nativeClassifier)
	if got := clf.UnclassifiedLabels(); len(got) != 1 || got[0] != "restricted" {
		t.Fatalf("default unclassified = %v", got)
	}
	labels, err := clf.ClassifyAction(context.Background(), Action{Tool: "other"})
	if err != nil || len(labels) != 1 || labels[0] != "restricted" {
		t.Fatalf("unlisted = %v %v", labels, err)
	}
	labels, err = clf.ClassifyAction(context.Background(), Action{Tool: "echo", Args: map[string]any{"text": "secret"}})
	if err != nil || len(labels) != 2 || labels[0] != "public" || labels[1] != "secret" {
		t.Fatalf("tool+pattern = %v %v", labels, err)
	}
	resultLabels, err := clf.ClassifyResult(context.Background(), Action{Tool: "echo"}, "has secret")
	if err != nil || len(resultLabels) != 1 || resultLabels[0] != "secret" {
		t.Fatalf("result = %v %v", resultLabels, err)
	}

	empty := NewClassifier("c", ClassifierConfig{Unclassified: []string{}}).(*nativeClassifier)
	labels, err = empty.ClassifyAction(context.Background(), Action{Tool: "other"})
	if err != nil || labels != nil {
		t.Fatalf("empty unclassified = %#v %v", labels, err)
	}
	bad, err := clf.ClassifyAction(context.Background(), Action{Tool: "echo", Args: map[string]any{"ch": make(chan int)}})
	if err != nil || len(bad) != 1 || bad[0] != "restricted" {
		t.Fatalf("encode fallback = %v %v", bad, err)
	}
}

func reqTool(name string, args map[string]any) Request {
	return Request{Action: Action{Tool: name, Args: args}}
}
