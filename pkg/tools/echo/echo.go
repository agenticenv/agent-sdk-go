package echo

import (
	"context"

	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools"
)

var _ interfaces.Tool = (*Echo)(nil)

// Echo echoes the input message back. Useful for testing the tool-calling flow.
type Echo struct{}

// New returns a new Echo tool.
func New() *Echo {
	return &Echo{}
}

func (*Echo) Name() string { return "echo" }

func (*Echo) Description() string {
	return "(Demo) Echoes the given message back. Use for testing tool-calling flow."
}

func (*Echo) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"message": tools.ParamString("The message to echo back"),
		},
		"message",
	)
}

func (*Echo) Execute(ctx context.Context, args map[string]any) (any, error) {
	msg, _ := args["message"].(string)
	return msg, nil
}
