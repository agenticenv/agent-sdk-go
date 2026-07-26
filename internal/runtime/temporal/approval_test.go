package temporal

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
	if parsed.Args["x"] != float64(1) && parsed.Args["x"] != 1 {
		t.Fatalf("unexpected args: %#v", parsed.Args)
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
		t.Fatalf("token: got %q want tok-map", token)
	}
	parsed, err := types.ParseToolApproval(req)
	if err != nil {
		t.Fatalf("ParseToolApproval: %v", err)
	}
	if parsed.ToolName != "search" || parsed.ApprovalToken != "tok-map" {
		t.Fatalf("unexpected parsed: %#v", parsed)
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

func TestPrepareApprovalFromCustomEvent_ParseError(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameToolApproval), 123)
	_, _, err := prepareApprovalFromCustomEvent(ev)
	if err == nil {
		t.Fatal("expected parse error for invalid value shape")
	}
}

// TestDelegationEventContainsToolCallID verifies that the ToolCallID field is present
// in AgentCustomEventDelegationValue and round-trips through JSON correctly.
func TestDelegationEventContainsToolCallID(t *testing.T) {
	ev := events.NewAgentCustomEvent(string(events.AgentCustomEventNameSubAgentDelegation),
		events.AgentCustomEventDelegationValue{
			AgentName:     "parent",
			SubAgentName:  "child",
			ToolCallID:    "tc-deleg-99",
			ApprovalToken: "tok",
		})

	raw, err := ev.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	decoded, err := events.EventFromJSON(raw)
	if err != nil {
		t.Fatalf("EventFromJSON: %v", err)
	}
	ce, ok := decoded.(*events.AgentCustomEvent)
	if !ok {
		t.Fatalf("expected *AgentCustomEvent, got %T", decoded)
	}
	val, err := events.ParseCustomEventDelegation(ce)
	if err != nil {
		t.Fatalf("ParseCustomEventDelegation: %v", err)
	}
	if val.ToolCallID != "tc-deleg-99" {
		t.Errorf("ToolCallID = %q, want tc-deleg-99", val.ToolCallID)
	}
}

// TestIsNotFoundError covers the string-matching heuristic for Temporal not-found errors.
func TestIsNotFoundError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("NOT_FOUND: activity task not found"), true},
		{errors.New("NotFound: resource gone"), true},
		{errors.New("activity task not found"), true},
		{errors.New("permission denied"), false},
		{errors.New("internal server error"), false},
	}
	for _, tc := range cases {
		got := isNotFoundError(tc.err)
		if got != tc.want {
			t.Errorf("isNotFoundError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
