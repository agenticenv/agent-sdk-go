package setup

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	cfg, err := LoadConfig(filepath.Join(root, "benchmarks", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Runs <= 0 {
		t.Fatalf("runs=%d", cfg.Agent.Runs)
	}
}
