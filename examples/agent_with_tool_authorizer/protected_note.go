package main

import (
	"context"
	"os"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
)

var (
	_ interfaces.Tool           = (*ProtectedNote)(nil)
	_ interfaces.ToolAuthorizer = (*ProtectedNote)(nil)
)

// ProtectedNote is a custom tool that demonstrates ToolAuthorizer.
type ProtectedNote struct{}

// NewProtectedNote returns a new ProtectedNote tool.
func NewProtectedNote() *ProtectedNote { return &ProtectedNote{} }

func (*ProtectedNote) Name() string { return "protected_note" }

func (*ProtectedNote) Description() string {
	return "Returns a protected internal note for a topic. Use when the user explicitly asks for the protected note or internal note."
}

func (*ProtectedNote) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"topic": tools.ParamString("The topic for the protected note"),
		},
		"topic",
	)
}

func (*ProtectedNote) Authorize(ctx context.Context, args map[string]any) (interfaces.ToolAuthorizationDecision, error) {
	if strings.TrimSpace(os.Getenv("ALLOW_PROTECTED_NOTE")) == "1" {
		return interfaces.ToolAuthorizationDecision{Allow: true}, nil
	}

	return interfaces.ToolAuthorizationDecision{
		Allow:  false,
		Reason: "missing required authorization: set ALLOW_PROTECTED_NOTE=1",
	}, nil
}

func (*ProtectedNote) Execute(ctx context.Context, args map[string]any) (any, error) {
	topic, _ := args["topic"].(string)
	topic = strings.TrimSpace(topic)
	if topic == "" {
		topic = "general"
	}

	return "Protected note for " + topic + ": rollout is limited to internal teams this week.", nil
}
