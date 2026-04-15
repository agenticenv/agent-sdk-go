package temporal

import (
	"context"
	"reflect"
	"testing"
)

type captureLogger struct {
	calls []struct {
		level string
		msg   string
		ctx   context.Context
		kvs   []any
	}
}

func (c *captureLogger) Debug(ctx context.Context, msg string, keyvals ...any) {
	c.record("debug", ctx, msg, keyvals)
}

func (c *captureLogger) Info(ctx context.Context, msg string, keyvals ...any) {
	c.record("info", ctx, msg, keyvals)
}

func (c *captureLogger) Warn(ctx context.Context, msg string, keyvals ...any) {
	c.record("warn", ctx, msg, keyvals)
}

func (c *captureLogger) Error(ctx context.Context, msg string, keyvals ...any) {
	c.record("error", ctx, msg, keyvals)
}

func (c *captureLogger) record(level string, ctx context.Context, msg string, keyvals []any) {
	c.calls = append(c.calls, struct {
		level string
		msg   string
		ctx   context.Context
		kvs   []any
	}{level, msg, ctx, append([]any(nil), keyvals...)})
}

func TestNewLogAdapter_ForwardsToSDKLogger(t *testing.T) {
	cap := &captureLogger{}
	ad := NewLogAdapter(cap)

	ad.Debug("debug-msg", "a", 1)
	ad.Info("info-msg")
	ad.Warn("warn-msg", "k", "v")
	ad.Error("err-msg", "x", false)

	if len(cap.calls) != 4 {
		t.Fatalf("got %d calls, want 4", len(cap.calls))
	}
	want := []struct {
		level string
		msg   string
		kvs   []any
	}{
		{"debug", "debug-msg", []any{"a", 1}},
		{"info", "info-msg", nil},
		{"warn", "warn-msg", []any{"k", "v"}},
		{"error", "err-msg", []any{"x", false}},
	}
	for i, w := range want {
		got := cap.calls[i]
		if got.level != w.level || got.msg != w.msg {
			t.Errorf("call %d: level=%q msg=%q, want level=%q msg=%q", i, got.level, got.msg, w.level, w.msg)
		}
		if got.ctx != context.Background() {
			t.Errorf("call %d: ctx = %v, want Background", i, got.ctx)
		}
		if !reflect.DeepEqual(got.kvs, w.kvs) {
			t.Errorf("call %d: kvs = %#v, want %#v", i, got.kvs, w.kvs)
		}
	}
}

func TestNewLogAdapter_NilUsesNoop(t *testing.T) {
	ad := NewLogAdapter(nil)
	// NoopLogger discards; we only assert no panic and a usable adapter.
	ad.Debug("d")
	ad.Info("i")
	ad.Warn("w")
	ad.Error("e")
}
