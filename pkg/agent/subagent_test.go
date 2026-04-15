package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewSubAgentTool_Execute(t *testing.T) {
	sub := &Agent{agentConfig: agentConfig{Name: "S", ID: "x"}}
	tool := NewSubAgentTool(sub)
	_, err := tool.Execute(context.Background(), map[string]any{"query": "hi"})
	if !errors.Is(err, ErrSubAgentToolNotExecutable) {
		t.Fatalf("Execute err = %v, want ErrSubAgentToolNotExecutable", err)
	}
}

func TestNewSubAgentTool_nilReturnsNil(t *testing.T) {
	if NewSubAgentTool(nil) != nil {
		t.Fatal("NewSubAgentTool(nil) should return nil")
	}
}

func TestNewSubAgentTool_Description(t *testing.T) {
	withDesc := &Agent{agentConfig: agentConfig{Name: "N", Description: "  math  "}}
	tool := NewSubAgentTool(withDesc).(*subAgentTool)
	if !strings.Contains(tool.Description(), "math") {
		t.Fatalf("got %q", tool.Description())
	}

	nameOnly := &Agent{agentConfig: agentConfig{Name: "Helper"}}
	tool2 := NewSubAgentTool(nameOnly).(*subAgentTool)
	if !strings.Contains(tool2.Description(), "Helper") {
		t.Fatalf("got %q", tool2.Description())
	}
}
