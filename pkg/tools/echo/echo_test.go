package echo

import (
	"context"
	"testing"

	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools"
)

func TestEcho_Name(t *testing.T) {
	e := New()
	if e.Name() != "echo" {
		t.Errorf("Name() = %q, want echo", e.Name())
	}
}

func TestEcho_Description(t *testing.T) {
	e := New()
	if e.Description() == "" {
		t.Error("Description should not be empty")
	}
}

func TestEcho_Parameters(t *testing.T) {
	e := New()
	p := e.Parameters()
	if p["type"] != "object" {
		t.Errorf("Parameters type = %v, want object", p["type"])
	}
	props, ok := p["properties"].(map[string]interfaces.JSONSchema)
	if !ok || props["message"] == nil {
		t.Error("Parameters should have message property")
	}
}

func TestEcho_Execute(t *testing.T) {
	e := New()
	ctx := context.Background()

	got, err := e.Execute(ctx, map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "hello" {
		t.Errorf("Execute = %v, want hello", got)
	}
}

func TestEcho_Execute_EmptyMessage(t *testing.T) {
	e := New()
	ctx := context.Background()

	got, err := e.Execute(ctx, map[string]any{"message": ""})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "" {
		t.Errorf("Execute = %v, want empty string", got)
	}
}

func TestEcho_Execute_MissingMessage(t *testing.T) {
	e := New()
	ctx := context.Background()

	got, err := e.Execute(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Echo returns empty string when message is missing (type assertion fails)
	if got != "" {
		t.Errorf("Execute with missing message = %v, want empty string", got)
	}
}

func TestEcho_ParamsSchema(t *testing.T) {
	e := New()
	p := e.Parameters()
	wantMsg := tools.ParamString("The message to echo back")
	props, ok := p["properties"].(map[string]interfaces.JSONSchema)
	if !ok || props == nil {
		t.Fatal("properties should not be nil")
	}
	msgSchema := props["message"]
	if msgSchema["type"] != wantMsg["type"] || msgSchema["description"] != wantMsg["description"] {
		t.Errorf("message schema = %v, want %v", msgSchema, wantMsg)
	}
}
