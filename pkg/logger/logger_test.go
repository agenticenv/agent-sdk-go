package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewWriterLogger_AddSource_UsesCallSiteNotWrapper(t *testing.T) {
	var buf bytes.Buffer
	log := NewWriterLogger(&buf, "info", "json", true)
	log.Info(context.Background(), "hello", "k", "v")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("json: %v", err)
	}
	src, ok := line["source"].(map[string]any)
	if !ok {
		t.Fatalf("missing source: %s", buf.String())
	}
	file, _ := src["file"].(string)
	if file == "" {
		t.Fatalf("source.file empty: %v", src)
	}
	if strings.Contains(file, "pkg/logger/logger.go") {
		t.Fatalf("source should be call site, got wrapper file: %q", file)
	}
	if !strings.Contains(file, "logger_test.go") {
		t.Fatalf("expected test file in source.file, got %q", file)
	}
}

func TestParseSlogLevel_viaNewTextLogger(t *testing.T) {
	t.Run("debug_and_trim_case", func(t *testing.T) {
		for _, level := range []string{"debug", "  DEBUG  "} {
			var buf bytes.Buffer
			NewTextLogger(&buf, level).Info(context.Background(), "msg")
			if !strings.Contains(buf.String(), "msg") {
				t.Errorf("level %q: buf=%q", level, buf.String())
			}
		}
	})
	t.Run("warn_and_warning_alias", func(t *testing.T) {
		for _, level := range []string{"warn", "warning", "  WARN  "} {
			var buf bytes.Buffer
			l := NewTextLogger(&buf, level)
			l.Info(context.Background(), "no")
			l.Warn(context.Background(), "yes")
			if strings.Contains(buf.String(), "no") {
				t.Errorf("level %q: info should be below threshold", level)
			}
			if !strings.Contains(buf.String(), "yes") {
				t.Errorf("level %q: buf=%q", level, buf.String())
			}
		}
	})
	t.Run("unknown_defaults_to_error_threshold", func(t *testing.T) {
		var buf bytes.Buffer
		l := NewTextLogger(&buf, "unknown-level")
		l.Info(context.Background(), "no")
		l.Error(context.Background(), "yes")
		if strings.Contains(buf.String(), "no") {
			t.Fatalf("info should not log: %q", buf.String())
		}
		if !strings.Contains(buf.String(), "yes") {
			t.Fatalf("buf=%q", buf.String())
		}
	})
	t.Run("empty_level_defaults_error", func(t *testing.T) {
		var buf bytes.Buffer
		l := NewTextLogger(&buf, "")
		l.Warn(context.Background(), "no")
		l.Error(context.Background(), "yes")
		if strings.Contains(buf.String(), "no") {
			t.Fatalf("warn should not log at default error: %q", buf.String())
		}
		if !strings.Contains(buf.String(), "yes") {
			t.Fatalf("buf=%q", buf.String())
		}
	})
}

func TestNewTextLogger_ErrorLevelAllowsError(t *testing.T) {
	var buf bytes.Buffer
	log := NewTextLogger(&buf, "error")
	log.Info(context.Background(), "no")
	log.Error(context.Background(), "yes", "k", "v")
	if strings.Contains(buf.String(), "no") {
		t.Fatalf("info should be filtered: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "yes") {
		t.Fatalf("expected error line: %q", buf.String())
	}
}

func TestNewWriterLogger_JSONFormatCaseInsensitive(t *testing.T) {
	var buf bytes.Buffer
	log := NewWriterLogger(&buf, "info", "JSON", false)
	log.Info(context.Background(), "x", slog.String("k", "v"))
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("json: %v body=%q", err, buf.String())
	}
	if line["msg"] != "x" {
		t.Fatalf("msg = %v", line["msg"])
	}
}

func TestNewWriterLogger_UnknownFormatDefaultsToText(t *testing.T) {
	var buf bytes.Buffer
	log := NewWriterLogger(&buf, "info", "yaml", false)
	log.Info(context.Background(), "plain")
	s := buf.String()
	if !strings.Contains(s, "plain") || !strings.Contains(s, "level=") {
		t.Fatalf("expected text handler output, got %q", s)
	}
}

func TestNewWriterLogger_NilWriterUsesDiscard(t *testing.T) {
	log := NewWriterLogger(nil, "info", "text", false)
	log.Info(context.Background(), "dropped")
}

func TestDefaultLogger_NoPanic(t *testing.T) {
	_ = DefaultLogger("")
	_ = DefaultLogger("INFO")
	_ = DefaultLogger("bogus")
}

func TestNewSlog_NilReturnsNoop(t *testing.T) {
	l := NewSlog(nil)
	var buf bytes.Buffer
	l.Info(context.Background(), "silent")
	if buf.Len() != 0 {
		t.Fatal("noop should not write")
	}
}

func TestNewSlog_WrapsLogger(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := NewSlog(base)
	l.Debug(context.Background(), "d")
	if !strings.Contains(buf.String(), "d") {
		t.Fatalf("buf=%q", buf.String())
	}
}

func TestSlogLogger_Slog_nilReceiver(t *testing.T) {
	var s *SlogLogger
	if s.Slog() != nil {
		t.Fatal("nil SlogLogger.Slog() should be nil")
	}
}

func TestSlogLogger_Slog_nonNil(t *testing.T) {
	var buf bytes.Buffer
	s := NewTextLogger(&buf, "info").(*SlogLogger)
	if s.Slog() == nil {
		t.Fatal("expected underlying slog.Logger")
	}
}

func TestSlogLogger_emit_skipsWhenLevelDisabled(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})
	s := &SlogLogger{log: slog.New(h), adjustCallerPC: true}
	s.Info(context.Background(), "filtered")
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
	s.Error(context.Background(), "shown")
	if !strings.Contains(buf.String(), "shown") {
		t.Fatalf("expected error line: %q", buf.String())
	}
}

func TestSlogLogger_emit_DebugAndWarnWithAdjustCallerPC(t *testing.T) {
	var buf bytes.Buffer
	s := &SlogLogger{
		log:            slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		adjustCallerPC: true,
	}
	ctx := context.Background()
	s.Debug(ctx, "dbg")
	s.Warn(ctx, "wrn")
	out := buf.String()
	if !strings.Contains(out, "dbg") || !strings.Contains(out, "wrn") {
		t.Fatalf("buf=%q", out)
	}
}

func TestDiscardHandler_interfaceMethods(t *testing.T) {
	var h discardHandler
	if err := h.Handle(context.Background(), slog.Record{}); err != nil {
		t.Fatal(err)
	}
	_ = h.WithAttrs([]slog.Attr{slog.String("k", "v")})
	_ = h.WithGroup("g")
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("discard Enabled should be false")
	}
}

func TestNoopLogger_and_NewDiscardLogger(t *testing.T) {
	for _, name := range []string{"Noop", "Discard"} {
		var l Logger
		if name == "Noop" {
			l = NoopLogger()
		} else {
			l = NewDiscardLogger()
		}
		ctx := context.Background()
		l.Debug(ctx, "d")
		l.Info(ctx, "i")
		l.Warn(ctx, "w")
		l.Error(ctx, "e")
	}
}

func TestSlogLogger_allLevels_directHandler(t *testing.T) {
	var buf bytes.Buffer
	s := NewTextLogger(&buf, "debug").(*SlogLogger)
	ctx := context.Background()
	s.Debug(ctx, "d")
	s.Info(ctx, "i")
	s.Warn(ctx, "w")
	s.Error(ctx, "e")
	out := buf.String()
	for _, sub := range []string{"d", "i", "w", "e"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("missing %q in output: %q", sub, out)
		}
	}
}
