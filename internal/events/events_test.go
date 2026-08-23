package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

func TestConstructorsAndRoundTripForAllEventTypes(t *testing.T) {
	tests := []struct {
		name         string
		event        AgentEvent
		wantType     AgentEventType
		assertFields func(t *testing.T, raw map[string]any)
	}{
		{
			name:     "run_started_with_parent",
			event:    NewAgentRunStartedEvent("thread-1", "run-1", "parent-1"),
			wantType: AgentEventTypeRunStarted,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["threadId"] != "thread-1" || raw["runId"] != "run-1" || raw["parentRunId"] != "parent-1" {
					t.Fatalf("unexpected run started payload: %#v", raw)
				}
			},
		},
		{
			name:     "run_finished_with_result",
			event:    NewAgentRunFinishedEvent("thread-2", "run-2", &types.AgentRunResult{Content: "ok"}),
			wantType: AgentEventTypeRunFinished,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["threadId"] != "thread-2" || raw["runId"] != "run-2" {
					t.Fatalf("unexpected run finished payload: %#v", raw)
				}
			},
		},
		{
			name:     "run_error_with_code",
			event:    NewAgentRunErrorEvent("boom", "ERR_TEST"),
			wantType: AgentEventTypeRunError,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["message"] != "boom" || raw["code"] != "ERR_TEST" {
					t.Fatalf("unexpected run error payload: %#v", raw)
				}
			},
		},
		{
			name:     "step_started",
			event:    NewAgentStepStartedEvent("plan"),
			wantType: AgentEventTypeStepStarted,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["stepName"] != "plan" {
					t.Fatalf("unexpected step started payload: %#v", raw)
				}
			},
		},
		{
			name:     "step_finished",
			event:    NewAgentStepFinishedEvent("plan"),
			wantType: AgentEventTypeStepFinished,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["stepName"] != "plan" {
					t.Fatalf("unexpected step finished payload: %#v", raw)
				}
			},
		},
		{
			name:     "text_message_start",
			event:    NewAgentTextMessageStartEvent("msg-1", "assistant"),
			wantType: AgentEventTypeTextMessageStart,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "msg-1" || raw["role"] != "assistant" {
					t.Fatalf("unexpected text start payload: %#v", raw)
				}
			},
		},
		{
			name:     "text_message_content",
			event:    NewAgentTextMessageContentEvent("msg-1", "hello"),
			wantType: AgentEventTypeTextMessageContent,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "msg-1" || raw["delta"] != "hello" {
					t.Fatalf("unexpected text content payload: %#v", raw)
				}
			},
		},
		{
			name:     "text_message_end",
			event:    NewAgentTextMessageEndEvent("msg-1"),
			wantType: AgentEventTypeTextMessageEnd,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "msg-1" {
					t.Fatalf("unexpected text end payload: %#v", raw)
				}
			},
		},
		{
			name:     "tool_call_start_with_parent",
			event:    NewAgentToolCallStartEvent("tc-1", "calculator", "msg-parent"),
			wantType: AgentEventTypeToolCallStart,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["toolCallId"] != "tc-1" || raw["toolCallName"] != "calculator" || raw["parentMessageId"] != "msg-parent" {
					t.Fatalf("unexpected tool start payload: %#v", raw)
				}
			},
		},
		{
			name:     "tool_call_args",
			event:    NewAgentToolCallArgsEvent("tc-1", "{\"x\":1}"),
			wantType: AgentEventTypeToolCallArgs,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["toolCallId"] != "tc-1" || raw["delta"] != "{\"x\":1}" {
					t.Fatalf("unexpected tool args payload: %#v", raw)
				}
			},
		},
		{
			name:     "tool_call_end",
			event:    NewAgentToolCallEndEvent("tc-1"),
			wantType: AgentEventTypeToolCallEnd,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["toolCallId"] != "tc-1" {
					t.Fatalf("unexpected tool end payload: %#v", raw)
				}
			},
		},
		{
			name:     "tool_call_result_with_role",
			event:    NewAgentToolCallResultEvent("msg-2", "tc-1", "42", "tool"),
			wantType: AgentEventTypeToolCallResult,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "msg-2" || raw["toolCallId"] != "tc-1" || raw["content"] != "42" || raw["role"] != "tool" {
					t.Fatalf("unexpected tool result payload: %#v", raw)
				}
			},
		},
		{
			name:     "reasoning_start",
			event:    NewAgentReasoningStartEvent("r-1"),
			wantType: AgentEventTypeReasoningStart,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "r-1" {
					t.Fatalf("unexpected reasoning start payload: %#v", raw)
				}
			},
		},
		{
			name:     "reasoning_message_start",
			event:    NewAgentReasoningMessageStartEvent("r-1", "reasoning"),
			wantType: AgentEventTypeReasoningMessageStart,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "r-1" || raw["role"] != "reasoning" {
					t.Fatalf("unexpected reasoning msg start payload: %#v", raw)
				}
			},
		},
		{
			name:     "reasoning_message_content",
			event:    NewAgentReasoningMessageContentEvent("r-1", "thinking"),
			wantType: AgentEventTypeReasoningMessageContent,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "r-1" || raw["delta"] != "thinking" {
					t.Fatalf("unexpected reasoning msg content payload: %#v", raw)
				}
			},
		},
		{
			name:     "reasoning_message_end",
			event:    NewAgentReasoningMessageEndEvent("r-1"),
			wantType: AgentEventTypeReasoningMessageEnd,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "r-1" {
					t.Fatalf("unexpected reasoning msg end payload: %#v", raw)
				}
			},
		},
		{
			name:     "reasoning_end",
			event:    NewAgentReasoningEndEvent("r-1"),
			wantType: AgentEventTypeReasoningEnd,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["messageId"] != "r-1" {
					t.Fatalf("unexpected reasoning end payload: %#v", raw)
				}
			},
		},
		{
			name:     "raw_event_with_source",
			event:    NewAgentRawEvent(map[string]any{"foo": "bar"}, "provider"),
			wantType: AgentEventTypeRaw,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["source"] != "provider" {
					t.Fatalf("unexpected raw payload: %#v", raw)
				}
			},
		},
		{
			name:     "custom_event",
			event:    NewAgentCustomEvent("tool_approval", map[string]any{"x": 1}),
			wantType: AgentEventTypeCustom,
			assertFields: func(t *testing.T, raw map[string]any) {
				t.Helper()
				if raw["name"] != "tool_approval" {
					t.Fatalf("unexpected custom payload: %#v", raw)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.event.Type() != tc.wantType {
				t.Fatalf("wrong type: got %s want %s", tc.event.Type(), tc.wantType)
			}
			if tc.event.Timestamp() == nil {
				t.Fatal("timestamp is nil")
			}

			data, err := tc.event.ToJSON()
			if err != nil {
				t.Fatalf("ToJSON error: %v", err)
			}

			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("json unmarshal error: %v", err)
			}

			if got := raw["type"]; got != string(tc.wantType) {
				t.Fatalf("wire type mismatch: got %v want %s", got, tc.wantType)
			}
			if _, ok := raw["timestamp"]; !ok {
				t.Fatalf("missing timestamp in payload: %#v", raw)
			}

			tc.assertFields(t, raw)

			decoded, err := EventFromJSON(data)
			if err != nil {
				t.Fatalf("EventFromJSON error: %v", err)
			}
			if decoded.Type() != tc.wantType {
				t.Fatalf("decoded wrong type: got %s want %s", decoded.Type(), tc.wantType)
			}
		})
	}
}

func TestRunFinishedResultRoundTrip(t *testing.T) {
	orig := NewAgentRunFinishedEvent("thread-1", "run-1", &types.AgentRunResult{
		Content:   "hello",
		AgentName: "agent-a",
		Model:     "gpt-test",
		LLMUsage:  &types.LLMUsage{TotalTokens: 42},
	})
	data, err := orig.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}

	decoded, err := EventFromJSON(data)
	if err != nil {
		t.Fatalf("EventFromJSON error: %v", err)
	}
	fin, ok := decoded.(*AgentRunFinishedEvent)
	if !ok || fin == nil {
		t.Fatalf("decoded type: %T", decoded)
	}
	if fin.Result == nil {
		t.Fatal("decoded Result is nil")
	}
	if fin.Result.Content != "hello" || fin.Result.AgentName != "agent-a" || fin.Result.Model != "gpt-test" {
		t.Fatalf("unexpected result: %#v", fin.Result)
	}
	if fin.Result.LLMUsage == nil || fin.Result.LLMUsage.TotalTokens != 42 {
		t.Fatalf("unexpected llm usage: %#v", fin.Result.LLMUsage)
	}
}

func TestEventTypeFromJSON(t *testing.T) {
	data := []byte(`{"type":"TOOL_CALL_END","toolCallId":"x"}`)
	typ, err := EventTypeFromJSON(data)
	if err != nil {
		t.Fatalf("EventTypeFromJSON error: %v", err)
	}
	if typ != AgentEventTypeToolCallEnd {
		t.Fatalf("wrong type: got %s", typ)
	}
}

func TestEventFromJSONErrors(t *testing.T) {
	_, err := EventFromJSON([]byte(`{`))
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}

	_, err = EventFromJSON([]byte(`{"type":"NOPE"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("expected unknown type error, got: %v", err)
	}
}

func TestOptionalFieldOmitBehavior(t *testing.T) {
	runStarted := NewAgentRunStartedEvent("thread", "run")
	data, err := runStarted.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}
	if strings.Contains(string(data), "parentRunId") {
		t.Fatalf("unexpected parentRunId in payload: %s", data)
	}

	runErr := NewAgentRunErrorEvent("msg", "")
	data, err = runErr.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}
	if strings.Contains(string(data), `"code"`) {
		t.Fatalf("unexpected code in payload: %s", data)
	}
}

func TestCustomEventTypedValueHelpers(t *testing.T) {
	approval := NewAgentCustomEventApprovalValue("calculator", "token-1")
	if approval.ToolName != "calculator" || approval.ApprovalToken != "token-1" {
		t.Fatalf("unexpected approval value: %#v", approval)
	}
	if _, err := approval.ToJSON(); err != nil {
		t.Fatalf("approval ToJSON error: %v", err)
	}

	delegation := NewAgentCustomEventDelegationValue("sub-agent", "token-2")
	if delegation.SubAgentName != "sub-agent" || delegation.ApprovalToken != "token-2" {
		t.Fatalf("unexpected delegation value: %#v", delegation)
	}
	if _, err := delegation.ToJSON(); err != nil {
		t.Fatalf("delegation ToJSON error: %v", err)
	}

	ev := NewAgentCustomEvent(string(AgentCustomEventNameToolApproval), map[string]any{
		"toolName":      "calculator",
		"approvalToken": "tok",
	})
	parsedApproval, err := ParseCustomEventApproval(ev)
	if err != nil {
		t.Fatalf("ParseCustomEventApproval error: %v", err)
	}
	if parsedApproval.ToolName != "calculator" || parsedApproval.ApprovalToken != "tok" {
		t.Fatalf("unexpected parsed approval: %#v", parsedApproval)
	}

	ev2 := NewAgentCustomEvent(string(AgentCustomEventNameSubAgentDelegation), &AgentCustomEventDelegationValue{
		SubAgentName:  "child",
		ApprovalToken: "tok-2",
	})
	parsedDelegation, err := ParseCustomEventDelegation(ev2)
	if err != nil {
		t.Fatalf("ParseCustomEventDelegation error: %v", err)
	}
	if parsedDelegation.SubAgentName != "child" || parsedDelegation.ApprovalToken != "tok-2" {
		t.Fatalf("unexpected parsed delegation: %#v", parsedDelegation)
	}

	ev3 := NewAgentCustomEvent(string(AgentCustomEventNameBudget), map[string]any{
		"agentName":     "budget-agent",
		"totalTokens":   int64(120),
		"costUsd":       0.01,
		"approvalToken": "tok-3",
	})
	parsedBudget, err := ParseCustomEventBudget(ev3)
	if err != nil {
		t.Fatalf("ParseCustomEventBudget error: %v", err)
	}
	if parsedBudget.AgentName != "budget-agent" || parsedBudget.TotalTokens != 120 || parsedBudget.ApprovalToken != "tok-3" {
		t.Fatalf("unexpected parsed budget: %#v", parsedBudget)
	}
}

func TestCustomEventParseErrors(t *testing.T) {
	if _, err := ParseCustomEventApproval(nil); err == nil {
		t.Fatal("expected nil custom event error")
	}

	var nilApproval *AgentCustomEventApprovalValue
	evNilPtr := NewAgentCustomEvent("tool_approval", nilApproval)
	if _, err := ParseCustomEventApproval(evNilPtr); err == nil || !strings.Contains(err.Error(), "nil custom value pointer") {
		t.Fatalf("expected nil pointer error, got: %v", err)
	}

	evBadShape := NewAgentCustomEvent("tool_approval", 123)
	if _, err := ParseCustomEventApproval(evBadShape); err == nil {
		t.Fatal("expected unmarshal custom value error")
	}

	evMarshalFail := NewAgentCustomEvent("tool_approval", map[string]any{"x": make(chan int)})
	if _, err := ParseCustomEventApproval(evMarshalFail); err == nil || !strings.Contains(err.Error(), "marshal custom value") {
		t.Fatalf("expected marshal custom value error, got: %v", err)
	}
}
