package logger

import (
	"bytes"
	"context"
	"encoding/json"
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
