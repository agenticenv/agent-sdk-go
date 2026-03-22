package calculator

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCalculator_Name(t *testing.T) {
	c := New()
	if c.Name() != "calculator" {
		t.Errorf("Name() = %q, want calculator", c.Name())
	}
}

func TestCalculator_Execute_Add(t *testing.T) {
	c := New()
	ctx := context.Background()

	got, err := c.Execute(ctx, map[string]any{
		"operation": "add",
		"a":         json.Number("3"),
		"b":         json.Number("5"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != int64(8) {
		t.Errorf("Execute add = %v, want 8", got)
	}
}

func TestCalculator_Execute_Subtract(t *testing.T) {
	c := New()
	ctx := context.Background()

	got, err := c.Execute(ctx, map[string]any{
		"operation": "subtract",
		"a":         float64(10),
		"b":         float64(3),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != int64(7) {
		t.Errorf("Execute subtract = %v, want 7", got)
	}
}

func TestCalculator_Execute_Multiply(t *testing.T) {
	c := New()
	ctx := context.Background()

	got, err := c.Execute(ctx, map[string]any{
		"operation": "multiply",
		"a":         4,
		"b":         7,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != int64(28) {
		t.Errorf("Execute multiply = %v, want 28", got)
	}
}

func TestCalculator_Execute_Divide(t *testing.T) {
	c := New()
	ctx := context.Background()

	got, err := c.Execute(ctx, map[string]any{
		"operation": "divide",
		"a":         float64(15),
		"b":         float64(4),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 15/4 = 3.75
	if f, ok := got.(float64); !ok || f != 3.75 {
		t.Errorf("Execute divide = %v, want 3.75", got)
	}
}

func TestCalculator_Execute_DivideByZero(t *testing.T) {
	c := New()
	ctx := context.Background()

	_, err := c.Execute(ctx, map[string]any{
		"operation": "divide",
		"a":         float64(1),
		"b":         float64(0),
	})
	if err == nil {
		t.Error("Execute divide by zero should return error")
	}
}

func TestCalculator_Execute_UnknownOperation(t *testing.T) {
	c := New()
	ctx := context.Background()

	_, err := c.Execute(ctx, map[string]any{
		"operation": "power",
		"a":         float64(2),
		"b":         float64(3),
	})
	if err == nil {
		t.Error("Execute unknown operation should return error")
	}
}

func TestCalculator_Execute_InvalidNumbers(t *testing.T) {
	c := New()
	ctx := context.Background()

	_, err := c.Execute(ctx, map[string]any{
		"operation": "add",
		"a":         "not a number",
		"b":         float64(1),
	})
	if err == nil {
		t.Error("Execute with invalid numbers should return error")
	}
}
