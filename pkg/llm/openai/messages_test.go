package openai

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	openaisdk "github.com/openai/openai-go/v3"
)

// OpenAI requires a non-empty function name on assistant tool calls. messagesToOpenAI
// must never leave Function.Name empty (invalid request / provider errors).
func TestMessagesToOpenAI_assistantToolFunctionNamesNeverEmpty(t *testing.T) {
	t.Parallel()

	t.Run("preserves non-empty tool name", func(t *testing.T) {
		t.Parallel()
		req := &interfaces.LLMRequest{
			Messages: []interfaces.Message{
				{Role: interfaces.MessageRoleUser, Content: "add 1 and 2"},
				{
					Role:    interfaces.MessageRoleAssistant,
					Content: "",
					ToolCalls: []*interfaces.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "add",
						Args:       map[string]any{"a": 1.0, "b": 2.0},
					}},
				},
				{
					Role:       interfaces.MessageRoleTool,
					Content:    `{"result":3}`,
					ToolCallID: "call-1",
					ToolName:   "add",
				},
			},
		}
		for _, name := range assistantToolFunctionNames(messagesToOpenAI(req)) {
			if name != "add" {
				t.Fatalf("expected assistant tool function name 'add', got %q", name)
			}
		}
	})

	t.Run("empty tool name falls back so request is valid", func(t *testing.T) {
		t.Parallel()
		req := &interfaces.LLMRequest{
			Messages: []interfaces.Message{
				{Role: interfaces.MessageRoleUser, Content: "add 1 and 2"},
				{
					Role:    interfaces.MessageRoleAssistant,
					Content: "",
					ToolCalls: []*interfaces.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "",
						Args:       map[string]any{"a": 1.0, "b": 2.0},
					}},
				},
				{
					Role:       interfaces.MessageRoleTool,
					Content:    `{"result":3}`,
					ToolCallID: "call-1",
					ToolName:   "",
				},
			},
		}
		names := assistantToolFunctionNames(messagesToOpenAI(req))
		if len(names) != 1 {
			t.Fatalf("expected one assistant tool call, got %d", len(names))
		}
		if names[0] != "tool" {
			t.Fatalf("expected fallback name 'tool' when ToolName is empty, got %q", names[0])
		}
	})
}

func assistantToolFunctionNames(messages []openaisdk.ChatCompletionMessageParamUnion) []string {
	var names []string
	for _, m := range messages {
		for _, tc := range m.GetToolCalls() {
			if fn := tc.GetFunction(); fn != nil && fn.Name != "" {
				names = append(names, fn.Name)
			}
		}
	}
	return names
}
