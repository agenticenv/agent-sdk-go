package agent

import (
	"context"
	"errors"
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
