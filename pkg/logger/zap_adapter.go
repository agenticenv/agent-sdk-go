package logger

import (
	"fmt"
	"strings"

	"go.temporal.io/sdk/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ZapLoggerConfig configures a zap logger. Extensible for future options (encoding, sampling, etc.).
type ZapLoggerConfig struct {
	Level  string // debug, info, warn, error
	Output string // stdout, stderr, or file path. Empty = stdout (backward compat)
}

var _ log.Logger = (*ZapAdapter)(nil)

// ZapAdapter implements go.temporal.io/sdk/log.Logger using zap.
type ZapAdapter struct {
	zl *zap.Logger
}

func parseLevel(s string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zap.DebugLevel
	case "info":
		return zap.InfoLevel
	case "warn", "warning":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	case "dpanic":
		return zap.DPanicLevel
	case "panic":
		return zap.PanicLevel
	case "fatal":
		return zap.FatalLevel
	default:
		return zap.InfoLevel
	}
}

func newZapLoggerConfig(level zapcore.Level, output string) zap.Config {
	encodeConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	return zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      false,
		Sampling:         nil,
		Encoding:         "console",
		EncoderConfig:    encodeConfig,
		OutputPaths:      []string{output},
		ErrorOutputPaths: []string{"stderr"},
	}
}

// NewZapLoggerWithConfig returns a zap logger from config. Level and Output can be extended later.
func NewZapLoggerWithConfig(cfg ZapLoggerConfig) *zap.Logger {
	if cfg.Output == "" {
		cfg.Output = "stdout"
	}
	if cfg.Level == "" {
		cfg.Level = "error"
	}
	level := parseLevel(cfg.Level)
	output := strings.TrimSpace(cfg.Output)
	zapCfg := newZapLoggerConfig(level, output)
	l, err := zapCfg.Build()
	if err != nil {
		panic("Unable to create zap logger: " + err.Error())
	}
	return l
}

// NewZapLogger returns a zap logger at debug level (legacy).
func NewZapLogger() *zap.Logger {
	return NewZapLoggerWithConfig(ZapLoggerConfig{Level: "debug"})
}

// NewZapAdapter returns a ZapAdapter that logs via zap. Use with NewZapLoggerWithConfig.
func NewZapAdapter(zapLogger *zap.Logger) *ZapAdapter {
	return &ZapAdapter{
		zl: zapLogger.WithOptions(zap.AddCallerSkip(1)),
	}
}

func (z *ZapAdapter) fields(keyvals []interface{}) []zap.Field {
	if len(keyvals)%2 != 0 {
		return []zap.Field{zap.Error(fmt.Errorf("odd number of keyvals pairs: %v", keyvals))}
	}

	var fields []zap.Field
	for i := 0; i < len(keyvals); i += 2 {
		key, ok := keyvals[i].(string)
		if !ok {
			key = fmt.Sprintf("%v", keyvals[i])
		}
		fields = append(fields, zap.Any(key, keyvals[i+1]))
	}

	return fields
}

func (z *ZapAdapter) Debug(msg string, keyvals ...interface{}) {
	z.zl.Debug(msg, z.fields(keyvals)...)
}

func (z *ZapAdapter) Info(msg string, keyvals ...interface{}) {
	z.zl.Info(msg, z.fields(keyvals)...)
}

func (z *ZapAdapter) Warn(msg string, keyvals ...interface{}) {
	z.zl.Warn(msg, z.fields(keyvals)...)
}

func (z *ZapAdapter) Error(msg string, keyvals ...interface{}) {
	z.zl.Error(msg, z.fields(keyvals)...)
}

func (z *ZapAdapter) With(keyvals ...interface{}) log.Logger {
	return &ZapAdapter{zl: z.zl.With(z.fields(keyvals)...)}
}

func (z *ZapAdapter) WithCallerSkip(skip int) log.Logger {
	return &ZapAdapter{zl: z.zl.WithOptions(zap.AddCallerSkip(skip))}
}
