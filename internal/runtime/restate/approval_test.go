package restate

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

func TestPrepareApprovalFromCustomEvent_NilEvent(t *testing.T) {
	req, token, err := prepareApprovalFromCustomEvent(nil)
	if req != nil || token != "" || err == nil {
		t.Fatalf("expected nil req/token and error, got req=%v token=%q err=%v", req, token, err)
	}
	if !strings.Contains(err.Error(), "nil custom event") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareApprovalFromCustomEvent_UnknownName(t *testing.T) {
	ev := events.NewAgentCustomEvent("other_custom", map[string]any{"x": 1})
	req, token, err := prepareApprovalFromCustomEvent(ev)
	if req != nil || token != "" {
		t.Fatalf("expected nil req/token, got req=%v token=%q", req, token)
	}
	if !errors.Is(err, ErrNotApprovalCustomEvent) {
		t.Fatalf("expected ErrNotApprovalCustomEvent, got %v", err)
	}
}

func TestPrepareApprovalFromCustomEvent_ToolApprovalTypedValue(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), &events.AgentCustomEventApprovalValue{
		AgentName:       "agent-a",
		ToolCallID:      "call-1",
		ToolName:        "calculator",
		ToolDisplayName: "Calculator",
		Args:            map[string]any{"x": 1},
		ApprovalToken:   "tok-tool",
	})

	req, token, err := prepareApprovalFromCustomEvent(ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-tool" {
		t.Fatalf("token: got %q want tok-tool", token)
	}
	if req == nil || req.Name != types.ApprovalRequestNameTool {
		t.Fatalf("unexpected req: %#v", req)
	}

	parsed, err := types.ParseToolApproval(req)
	if err != nil {
		t.Fatalf("ParseToolApproval: %v", err)
	}
	if parsed.AgentName != "agent-a" || parsed.ToolCallID != "call-1" || parsed.ToolName != "calculator" ||
		parsed.ToolDisplayName != "Calculator" || parsed.ApprovalToken != "tok-tool" {
		t.Fatalf("unexpected parsed tool approval: %#v", parsed)
	}
}

func TestPrepareApprovalFromCustomEvent_Delegation(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameSubAgentDelegation), map[string]any{
		"agentName":     "parent",
		"subAgentName":  "child",
		"approvalToken": "tok-delegate",
		"args":          map[string]any{"task": "summarize"},
	})

	req, token, err := prepareApprovalFromCustomEvent(ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-delegate" {
		t.Fatalf("token: got %q want tok-delegate", token)
	}
	if req == nil || req.Name != types.ApprovalRequestNameSubAgent {
		t.Fatalf("unexpected req: %#v", req)
	}

	parsed, err := types.ParseDelegationApproval(req)
	if err != nil {
		t.Fatalf("ParseDelegationApproval: %v", err)
	}
	if parsed.AgentName != "parent" || parsed.SubAgentName != "child" || parsed.ApprovalToken != "tok-delegate" {
		t.Fatalf("unexpected parsed delegation: %#v", parsed)
	}
}

func TestPrepareApprovalFromCustomEvent_Budget(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameBudget), map[string]any{
		"agentName":     "budget-agent",
		"detail":        "tokens over",
		"totalTokens":   float64(150),
		"costUsd":       0.02,
		"approvalToken": "tok-budget",
	})

	req, token, err := prepareApprovalFromCustomEvent(ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-budget" {
		t.Fatalf("token: got %q want tok-budget", token)
	}
	if req == nil || req.Name != types.ApprovalRequestNameBudget {
		t.Fatalf("unexpected req: %#v", req)
	}
	parsed, err := types.ParseBudgetApproval(req)
	if err != nil {
		t.Fatalf("ParseBudgetApproval: %v", err)
	}
	if parsed.AgentName != "budget-agent" || parsed.TotalTokens != 150 || parsed.ApprovalToken != "tok-budget" {
		t.Fatalf("unexpected parsed budget: %#v", parsed)
	}
}

func TestPrepareApprovalFromCustomEvent_ClonesArgs(t *testing.T) {
	args := map[string]any{"k": "v"}
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), &events.AgentCustomEventApprovalValue{
		ToolName:      "t",
		ApprovalToken: "tok",
		Args:          args,
	})

	req, _, err := prepareApprovalFromCustomEvent(ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parsed, err := types.ParseToolApproval(req)
	if err != nil {
		t.Fatalf("ParseToolApproval: %v", err)
	}
	parsed.Args["k"] = "mutated"
	if args["k"] != "v" {
		t.Fatal("expected args map to be cloned, source was mutated")
	}
}

func TestPrepareApprovalFromCustomEvent_ToolApprovalMapValue(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), map[string]any{
		"toolName":      "search",
		"approvalToken": "tok-map",
		"args":          map[string]any{"q": "hello"},
	})
	req, token, err := prepareApprovalFromCustomEvent(ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-map" {
		t.Fatalf("token: got %q", token)
	}
	parsed, err := types.ParseToolApproval(req)
	if err != nil {
		t.Fatalf("ParseToolApproval: %v", err)
	}
	if parsed.ToolName != "search" {
		t.Fatalf("unexpected: %#v", parsed)
	}
}

func TestPrepareApprovalFromCustomEvent_ParseError(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), 123)
	_, _, err := prepareApprovalFromCustomEvent(ev)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDelegationEvent_ToolCallIDRoundTrip(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameSubAgentDelegation),
		events.AgentCustomEventDelegationValue{
			AgentName:     "parent",
			SubAgentName:  "child",
			ToolCallID:    "tc-99",
			ApprovalToken: "tok",
		})
	raw, err := ev.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := events.EventFromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	ce, ok := parsed.(*events.AgentCustomEvent)
	if !ok {
		t.Fatalf("type %T", parsed)
	}
	req, _, err := prepareApprovalFromCustomEvent(ce)
	if err != nil {
		t.Fatal(err)
	}
	del, err := types.ParseDelegationApproval(req)
	if err != nil {
		t.Fatal(err)
	}
	if del.SubAgentName != "child" {
		t.Fatalf("%#v", del)
	}
}
