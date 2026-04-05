package gemini

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"google.golang.org/genai"
)

// Gemini's API rejects requests where a tool result has an empty function_response.name
// (INVALID_ARGUMENT: Name cannot be empty). messagesToGemini must always set a non-empty name.
func TestMessagesToGemini_toolFunctionResponseNamesNeverEmpty(t *testing.T) {
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
		for _, name := range functionResponseNames(messagesToGemini(req)) {
			if name != "add" {
				t.Fatalf("expected function response name 'add', got %q", name)
			}
		}
	})

	t.Run("empty tool name falls back so API is valid", func(t *testing.T) {
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
					ToolName:   "",
				},
			},
		}
		names := functionResponseNames(messagesToGemini(req))
		if len(names) != 1 {
			t.Fatalf("expected one function response, got %d", len(names))
		}
		if names[0] != "tool" {
			t.Fatalf("expected fallback name 'tool' when ToolName is empty, got %q", names[0])
		}
	})
}

func functionResponseNames(contents []*genai.Content) []string {
	var names []string
	for _, c := range contents {
		for _, p := range c.Parts {
			if p != nil && p.FunctionResponse != nil {
				names = append(names, p.FunctionResponse.Name)
			}
		}
	}
	return names
}
