package anthropic

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/anthropics/anthropic-sdk-go"
)

// Anthropic requires a non-empty name on tool_use blocks. messagesToAnthropic must
// always set a non-empty tool name on assistant tool_use content.
func TestMessagesToAnthropic_toolUseNamesNeverEmpty(t *testing.T) {
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
		for _, name := range toolUseNamesFromMessages(messagesToAnthropic(req)) {
			if name != "add" {
				t.Fatalf("expected tool_use name 'add', got %q", name)
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
		names := toolUseNamesFromMessages(messagesToAnthropic(req))
		if len(names) != 1 {
			t.Fatalf("expected one tool_use block, got %d", len(names))
		}
		if names[0] != "tool" {
			t.Fatalf("expected fallback name 'tool' when ToolName is empty, got %q", names[0])
		}
	})
}

func toolUseNamesFromMessages(msgs []anthropic.MessageParam) []string {
	var names []string
	for _, m := range msgs {
		if m.Role != anthropic.MessageParamRoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if b.OfToolUse != nil {
				names = append(names, b.OfToolUse.Name)
			}
		}
	}
	return names
}
