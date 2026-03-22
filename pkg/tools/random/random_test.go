package random

import (
	"context"
	"testing"
)

func TestRandom_Name(t *testing.T) {
	r := New()
	if r.Name() != "random" {
		t.Errorf("Name() = %q, want random", r.Name())
	}
}

func TestRandom_Execute_Range(t *testing.T) {
	rd := New()
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		got, err := rd.Execute(ctx, map[string]any{"min": 1, "max": 6})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		n, ok := got.(int)
		if !ok {
			t.Fatalf("Execute = %T, want int", got)
		}
		if n < 1 || n > 6 {
			t.Errorf("Execute = %d, want 1-6", n)
		}
	}
}

func TestRandom_Execute_SingleValue(t *testing.T) {
	rd := New()
	ctx := context.Background()

	got, err := rd.Execute(ctx, map[string]any{"min": 5, "max": 5})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != 5 {
		t.Errorf("Execute(min=5,max=5) = %v, want 5", got)
	}
}

func TestRandom_Execute_MinGreaterThanMax(t *testing.T) {
	rd := New()
	ctx := context.Background()

	_, err := rd.Execute(ctx, map[string]any{"min": 10, "max": 1})
	if err == nil {
		t.Error("Execute with min > max should return error")
	}
}

func TestRandom_Execute_InvalidRange(t *testing.T) {
	rd := New()
	ctx := context.Background()

	_, err := rd.Execute(ctx, map[string]any{"min": "x", "max": 5})
	if err == nil {
		t.Error("Execute with invalid min should return error")
	}

	_, err = rd.Execute(ctx, map[string]any{"min": 1, "max": "y"})
	if err == nil {
		t.Error("Execute with invalid max should return error")
	}
}
