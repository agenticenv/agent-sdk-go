// Package logger defines the SDK Logger interface and slog-backed implementations.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"
)

// Logger is the SDK logging contract (log/slog-style): pass slog.String, slog.Int, slog.Any, etc. as keyvals.
// Use DefaultLogger, NoopLogger, NewSlog, NewTextLogger, or NewWriterLogger from this package.
type Logger interface {
	Debug(ctx context.Context, msg string, keyvals ...any)
	Info(ctx context.Context, msg string, keyvals ...any)
	Warn(ctx context.Context, msg string, keyvals ...any)
	Error(ctx context.Context, msg string, keyvals ...any)
}

// Ensure SlogLogger implements Logger.
var _ Logger = (*SlogLogger)(nil)

// SlogLogger wraps a *slog.Logger and implements Logger.
type SlogLogger struct {
	log *slog.Logger
	// adjustCallerPC, when true, emits records with PC past this wrapper so
	// HandlerOptions.AddSource points at SDK call sites (not pkg/logger).
	adjustCallerPC bool
}

func (s *SlogLogger) Debug(ctx context.Context, msg string, keyvals ...any) {
	if s.adjustCallerPC {
		s.emit(ctx, slog.LevelDebug, msg, keyvals...)
		return
	}
	s.log.DebugContext(ctx, msg, keyvals...)
}

func (s *SlogLogger) Info(ctx context.Context, msg string, keyvals ...any) {
	if s.adjustCallerPC {
		s.emit(ctx, slog.LevelInfo, msg, keyvals...)
		return
	}
	s.log.InfoContext(ctx, msg, keyvals...)
}

func (s *SlogLogger) Warn(ctx context.Context, msg string, keyvals ...any) {
	if s.adjustCallerPC {
		s.emit(ctx, slog.LevelWarn, msg, keyvals...)
		return
	}
	s.log.WarnContext(ctx, msg, keyvals...)
}

func (s *SlogLogger) Error(ctx context.Context, msg string, keyvals ...any) {
	if s.adjustCallerPC {
		s.emit(ctx, slog.LevelError, msg, keyvals...)
		return
	}
	s.log.ErrorContext(ctx, msg, keyvals...)
}

// emit mirrors slog.Logger.log but uses a deeper caller skip so "source" is the
// real caller when the handler has AddSource (see adjustCallerPC).
// Slog returns the underlying *slog.Logger used by this logger.
func (s *SlogLogger) Slog() *slog.Logger {
	if s == nil {
		return nil
	}
	return s.log
}

func (s *SlogLogger) emit(ctx context.Context, level slog.Level, msg string, args ...any) {
	if !s.log.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	// skip: runtime.Callers, emit, Info/Debug/Warn/Error → next frame is SDK caller
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(args...)
	if ctx == nil {
		ctx = context.Background()
	}
	_ = s.log.Handler().Handle(ctx, r)
}

// DefaultLogger returns an SDK logger that writes human-readable lines to stderr.
// level uses slog names: debug, info, warn, error (case-insensitive). Empty defaults to "error".
func DefaultLogger(level string) Logger {
	lvl := parseSlogLevel(level)
	opts := &slog.HandlerOptions{Level: lvl}
	h := slog.NewTextHandler(os.Stderr, opts)
	return &SlogLogger{log: slog.New(h), adjustCallerPC: false}
}

// NewSlog returns a Logger wrapping the given *slog.Logger.
func NewSlog(l *slog.Logger) Logger {
	if l == nil {
		return NoopLogger()
	}
	return &SlogLogger{log: l}
}

// discardHandler implements slog.Handler that drops everything.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

var _ slog.Handler = discardHandler{}

// NoopLogger returns a logger that discards all output. Use with agent.WithLogger to silence the SDK.
func NoopLogger() Logger {
	return &SlogLogger{log: slog.New(discardHandler{}), adjustCallerPC: false}
}

// NewDiscardLogger is an alias for NoopLogger.
func NewDiscardLogger() Logger { return NoopLogger() }

// parseSlogLevel maps config strings (debug, info, warn, error) to slog.Level.
func parseSlogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelError
	}
}

// NewTextLogger writes text lines to w at the given level without caller source (stable for tests).
func NewTextLogger(w io.Writer, level string) Logger {
	return newWriterLogger(w, level, "text", false)
}

// NewWriterLogger writes to w using slog's text or JSON handler.
// format is "text" or "json" (case-insensitive); any other value defaults to "text".
// addSource adds file:line (slog source field) to each record when true.
func NewWriterLogger(w io.Writer, level, format string, addSource bool) Logger {
	return newWriterLogger(w, level, format, addSource)
}

func newWriterLogger(w io.Writer, level, format string, addSource bool) Logger {
	if w == nil {
		w = io.Discard
	}
	opts := &slog.HandlerOptions{
		Level:     parseSlogLevel(level),
		AddSource: addSource,
	}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		h = slog.NewTextHandler(w, opts)
	}
	return &SlogLogger{log: slog.New(h), adjustCallerPC: addSource}
}
