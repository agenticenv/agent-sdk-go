package setup

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/memory"
)

func TestLoadConfig(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	cfg, err := LoadConfig(filepath.Join(root, "eval-harness", "runner", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UserPrompt == "" && !cfg.Memory.Enabled {
		t.Fatal("empty prompt")
	}
}

func TestParseMemoryStoreMode(t *testing.T) {
	t.Parallel()

	mode, err := ParseMemoryStoreMode("always")
	if err != nil || mode != memory.StoreModeAlways {
		t.Fatalf("ParseMemoryStoreMode(always) = %q, %v", mode, err)
	}

	mode, err = ParseMemoryStoreMode("")
	if err != nil || mode != memory.StoreModeOnDemand {
		t.Fatalf("ParseMemoryStoreMode(empty) = %q, %v", mode, err)
	}

	if _, err := ParseMemoryStoreMode("invalid"); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}
