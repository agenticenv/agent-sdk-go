package main

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
)

var _ interfaces.Tool = (*Reverser)(nil)

// Reverser reverses the input string. Example custom tool implementation.
type Reverser struct{}

// NewReverser returns a new Reverser tool.
func NewReverser() *Reverser { return &Reverser{} }

func (*Reverser) Name() string { return "reverser" }

func (*Reverser) Description() string {
	return "Reverses the given string. Use when the user wants text reversed or backwards."
}

func (*Reverser) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"text": tools.ParamString("The text to reverse"),
		},
		"text",
	)
}

func (*Reverser) Execute(ctx context.Context, args map[string]any) (any, error) {
	text, _ := args["text"].(string)
	runes := []rune(text)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes), nil
}
