package llm

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func TestBuildConfig_RequiresAPIKey(t *testing.T) {
	_, err := BuildConfig(WithLogger(logger.NoopLogger()))
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "APIKey is required" {
		t.Fatalf("got %v", err)
	}
}

func TestBuildConfig_DefaultsLogLevelAndLogger(t *testing.T) {
	c, err := BuildConfig(WithAPIKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "error" {
		t.Fatalf("LogLevel = %q", c.LogLevel)
	}
	if c.Logger == nil {
		t.Fatal("expected default logger")
	}
	if c.APIKey != "k" {
		t.Fatalf("APIKey = %q", c.APIKey)
	}
}

func TestBuildConfig_WithOptions(t *testing.T) {
	log := logger.NoopLogger()
	c, err := BuildConfig(
		WithAPIKey("secret"),
		WithModel("m1"),
		WithBaseURL("https://example/v1"),
		WithLogLevel("debug"),
		WithLogger(log),
	)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "m1" || c.BaseURL != "https://example/v1" || c.LogLevel != "debug" {
		t.Fatalf("cfg = %+v", c)
	}
	if c.Logger != log {
		t.Fatal("expected injected logger")
	}
}
