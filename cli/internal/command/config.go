package command

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
	"gopkg.in/yaml.v3"
)

// ConfigPathFlag carries the raw --config flag value (possibly empty) down
// to subcommands that need to resolve or report the config path.
type ConfigPathFlag string

// ConfigSample is the embedded default.yaml, injected by main.
type ConfigSample []byte

// ConfigCmd is `agctl config`.
type ConfigCmd struct {
	Path ConfigPathCmd `cmd:"" help:"Print XDG path for optional YAML overrides (merged onto defaults when present)."`
	Show ConfigShowCmd `cmd:"" help:"Print applicable config for the current runtime (secrets redacted)."`
	Edit ConfigEditCmd `cmd:"" help:"Open config in $EDITOR (creates XDG file on save if missing)."`
}

// ConfigPathCmd is `agctl config path` — always the XDG file, not --config overrides.
// The file is optional: absent means embedded defaults (+ env/flags) only.
type ConfigPathCmd struct{}

func (ConfigPathCmd) Run() error {
	path := config.XDGConfigPath()
	fmt.Println(path)
	if config.FileExists(path) {
		fmt.Fprintf(os.Stderr, "present — merged onto embedded defaults\n")
	} else {
		fmt.Fprintf(os.Stderr, "absent — optional; create this file (or use config edit) to override defaults\n")
	}
	return nil
}

// ConfigEditCmd is `agctl config edit`.
type ConfigEditCmd struct{}

func (ConfigEditCmd) Run(sample ConfigSample, raw ConfigPathFlag) error {
	return editConfig(sample, raw)
}

// editConfig opens the active config in $EDITOR.
// If the file does not exist yet, starts from the embedded default and writes
// it on save (including an unmodified default).
func editConfig(embedded ConfigSample, raw ConfigPathFlag) error {
	if len(embedded) == 0 {
		return fmt.Errorf("embedded default config is empty")
	}
	target := editTargetPath(raw)
	existed := config.FileExists(target)

	var initial []byte
	if existed {
		b, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("read config %q: %w", target, err)
		}
		initial = b
	} else {
		initial = append([]byte(nil), embedded...)
	}

	tmp, err := os.CreateTemp("", "agctl-config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(initial); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := runEditor(tmpPath); err != nil {
		return err
	}

	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("read edited config: %w", err)
	}
	if len(bytes.TrimSpace(edited)) == 0 {
		return fmt.Errorf("edited config is empty; not writing")
	}
	if existed && bytes.Equal(edited, initial) {
		fmt.Fprintf(os.Stderr, "no changes\n")
		return nil
	}

	writePath := target
	if !existed && strings.TrimSpace(string(raw)) == "" && strings.TrimSpace(os.Getenv("AGCTL_CONFIG")) == "" {
		// Ensure XDG parent dirs exist via adrg/xdg.
		writePath = config.DefaultConfigPath()
	} else if dir := filepath.Dir(writePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
	}
	if err := os.WriteFile(writePath, edited, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if !existed {
		fmt.Printf("No config found — creating default config at %s\n", writePath)
	} else {
		fmt.Printf("Wrote %s\n", writePath)
	}
	return nil
}

func editTargetPath(raw ConfigPathFlag) string {
	if p := config.ExplicitConfigPath(string(raw)); p != "" {
		return p
	}
	return config.XDGConfigPath()
}

func runEditor(path string) error {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "vi"
		}
	}
	fields := strings.Fields(editor)
	args := append(fields[1:], path)
	cmd := exec.Command(fields[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q: %w", editor, err)
	}
	return nil
}

// ConfigShowCmd is `agctl config show`.
type ConfigShowCmd struct{}

func (ConfigShowCmd) Run(cfg *config.Config) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	if err := enc.Encode(config.ForShow(cfg)); err != nil {
		return err
	}
	return enc.Close()
}
