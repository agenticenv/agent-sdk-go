package currenttime

import (
	"context"
	"regexp"
	"testing"
	"time"
)

var rfc3339Re = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

func TestCurrentTime_Name(t *testing.T) {
	ct := New()
	if ct.Name() != "current_time" {
		t.Errorf("Name() = %q, want current_time", ct.Name())
	}
}

func TestCurrentTime_Execute_UTC(t *testing.T) {
	ct := New()
	ctx := context.Background()

	got, err := ct.Execute(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("Execute = %T, want string", got)
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("Execute returned invalid RFC3339: %q", s)
	}
	if !rfc3339Re.MatchString(s) {
		t.Errorf("Execute = %q, want RFC3339 format", s)
	}
}

func TestCurrentTime_Execute_WithTimezone(t *testing.T) {
	ct := New()
	ctx := context.Background()

	got, err := ct.Execute(ctx, map[string]any{"timezone": "America/New_York"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("Execute = %T, want string", got)
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("Execute returned invalid RFC3339: %q", s)
	}
}

func TestCurrentTime_Execute_InvalidTimezone(t *testing.T) {
	ct := New()
	ctx := context.Background()

	got, err := ct.Execute(ctx, map[string]any{"timezone": "Invalid/Timezone"})
	if err != nil {
		t.Fatalf("Execute with invalid tz should fallback to UTC, got: %v", err)
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("Execute = %T, want string", got)
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("Execute should return valid RFC3339 fallback: %q", s)
	}
}
