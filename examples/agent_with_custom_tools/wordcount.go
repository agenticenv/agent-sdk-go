package main

import (
	"context"
	"strings"

	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools"
)

var _ interfaces.Tool = (*WordCount)(nil)

// WordCount counts words in the input string. Example custom tool implementation.
type WordCount struct{}

// NewWordCount returns a new WordCount tool.
func NewWordCount() *WordCount { return &WordCount{} }

func (*WordCount) Name() string { return "word_count" }

func (*WordCount) Description() string {
	return "Counts the number of words in the given text. Use when the user asks for word count."
}

func (*WordCount) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"text": tools.ParamString("The text to count words in"),
		},
		"text",
	)
}

func (*WordCount) Execute(ctx context.Context, args map[string]any) (any, error) {
	text, _ := args["text"].(string)
	fields := strings.Fields(strings.TrimSpace(text))
	return len(fields), nil
}
