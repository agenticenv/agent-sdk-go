package guardrail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

func TestRegexDeny(t *testing.T) {
	g, err := RegexDeny("pii", `secret`, "no secrets", StageInput)
	if err != nil {
		t.Fatal(err)
	}
	if g.Name() != "pii" || !sameStages(g.Stages(), StageInput) {
		t.Fatalf("name/stages = %s %v", g.Name(), g.Stages())
	}
	dec, err := g.Evaluate(context.Background(), Input{Text: "a secret b"})
	if err != nil || dec.Action != ActionDeny || dec.Reason != "no secrets" {
		t.Fatalf("deny = %+v %v", dec, err)
	}
	dec, err = g.Evaluate(context.Background(), Input{Text: "clean"})
	if err != nil || dec.Action != ActionAllow {
		t.Fatalf("allow = %+v %v", dec, err)
	}
	if _, err := RegexDeny("x", "", "r", StageInput); err == nil || !strings.Contains(err.Error(), "pattern is required") {
		t.Fatalf("empty pattern: %v", err)
	}
	if _, err := RegexDeny("x", "(", "r", StageInput); err == nil {
		t.Fatal("expected invalid pattern error")
	}
}

func TestRegexRedact(t *testing.T) {
	g, err := RegexRedact("redact", `secret`, "[x]", StageOutput)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := g.Evaluate(context.Background(), Input{Text: "secret and secret"})
	if err != nil || dec.Action != ActionModify || dec.Text != "[x] and [x]" || dec.Reason != "redacted" {
		t.Fatalf("modify = %+v %v", dec, err)
	}
	dec, err = g.Evaluate(context.Background(), Input{Text: "clean"})
	if err != nil || dec.Action != ActionAllow {
		t.Fatalf("allow = %+v %v", dec, err)
	}
	empty, err := RegexRedact("wipe", `secret`, "", StageOutput)
	if err != nil {
		t.Fatal(err)
	}
	dec, err = empty.Evaluate(context.Background(), Input{Text: "secret"})
	if err != nil || dec.Action != ActionModify || dec.Text != "" {
		t.Fatalf("empty replacement = %+v %v", dec, err)
	}
	if _, err := RegexRedact("x", "", "y"); err == nil {
		t.Fatal("expected empty pattern error")
	}
}

func TestMaxLength(t *testing.T) {
	g, err := MaxLength("cap", 2, StageInput, StageOutput)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStages(g.Stages(), StageInput, StageOutput) {
		t.Fatalf("stages = %v", g.Stages())
	}
	dec, err := g.Evaluate(context.Background(), Input{Text: "éé"})
	if err != nil || dec.Action != ActionAllow {
		t.Fatalf("two runes = %+v %v", dec, err)
	}
	dec, err = g.Evaluate(context.Background(), Input{Text: "éée"})
	if err != nil || dec.Action != ActionDeny || dec.Reason != "exceeds max length of 2 runes" {
		t.Fatalf("deny = %+v %v", dec, err)
	}
	if _, err := MaxLength("cap", 0); err == nil {
		t.Fatal("expected maxRunes error")
	}
}

func TestDenyList(t *testing.T) {
	g, err := DenyList("words", []string{"Cat", " "}, StageInput)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := g.Evaluate(context.Background(), Input{Text: "a CAT"})
	if err != nil || dec.Action != ActionDeny || dec.Reason != "contains denied term" {
		t.Fatalf("deny = %+v %v", dec, err)
	}
	dec, err = g.Evaluate(context.Background(), Input{Text: "dog"})
	if err != nil || dec.Action != ActionAllow {
		t.Fatalf("allow = %+v %v", dec, err)
	}
	space, err := DenyList("space", []string{" "}, StageInput)
	if err != nil {
		t.Fatal(err)
	}
	dec, err = space.Evaluate(context.Background(), Input{Text: "a b"})
	if err != nil || dec.Action != ActionDeny {
		t.Fatalf("space = %+v %v", dec, err)
	}
	if _, err := DenyList("x", nil, StageInput); err == nil {
		t.Fatal("expected empty list error")
	}
	if _, err := DenyList("x", []string{"ok", ""}, StageInput); err == nil {
		t.Fatal("expected empty term error")
	}
}

func TestJSON(t *testing.T) {
	g := JSON("json")
	if g.Name() != "json" || !sameStages(g.Stages(), StageOutput) {
		t.Fatalf("name/stages = %s %v", g.Name(), g.Stages())
	}
	dec, err := g.Evaluate(context.Background(), Input{Text: "not json", Final: false})
	if err != nil || dec.Action != ActionAllow {
		t.Fatalf("non-final = %+v %v", dec, err)
	}
	dec, err = g.Evaluate(context.Background(), Input{Text: "  {\"a\":1}  ", Final: true})
	if err != nil || dec.Action != ActionAllow {
		t.Fatalf("valid = %+v %v", dec, err)
	}
	dec, err = g.Evaluate(context.Background(), Input{Text: "nope", Final: true})
	if err != nil || dec.Action != ActionDeny || dec.Reason != "invalid json" {
		t.Fatalf("invalid = %+v %v", dec, err)
	}
}

func TestNewNilFunc(t *testing.T) {
	g := New("custom", nil, StageInput)
	_, err := g.Evaluate(context.Background(), Input{})
	if err == nil || err.Error() != "guardrail: nil evaluate function" {
		t.Fatalf("got %v", err)
	}
}

func TestErrorIsSentinel(t *testing.T) {
	err := &Error{Guardrail: "pii", Stage: StageInput, Reason: "no"}
	if !errors.Is(err, ErrBlocked) || !errors.Is(err, types.ErrGuardrailBlocked) {
		t.Fatal("expected sentinel")
	}
	var got *Error
	if !errors.As(err, &got) || got.Guardrail != "pii" || got.Stage != StageInput {
		t.Fatalf("as = %+v", got)
	}
	want := `agent: guardrail blocked: "pii" (input): no`
	if err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
}

func sameStages(got []Stage, want ...Stage) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
