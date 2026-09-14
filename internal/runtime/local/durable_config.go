package local

import (
	"strings"
	"time"

	durable "github.com/agenticenv/durable-go"
)

// LocalConfig configures durable-go-backed execution for [LocalRuntime]. The zero value
// (and not calling [WithLocalConfig] at all) both mean the same thing: local runtime is
// durable by default, using an engine constructed from these (zero-value) defaults.
type LocalConfig struct {
	// Durability enables durable-go-backed execution. nil (the zero value, and the
	// implicit default when [WithLocalConfig] is never called) means true — local runtime
	// is durable by default. Set to a pointer to false (see [DurabilityOff] in
	// pkg/agent/runtime/local) to opt out and use the original pure in-memory execution path.
	Durability *bool

	// Engine, when set, is used as-is instead of constructing one from DataDir/knobs below.
	// The caller retains ownership: LocalRuntime.Close does not close a caller-supplied
	// Engine. Takes priority over every field below. Multiple agents/tasks may share one
	// Engine (see [durable.RegisterTask] — one taskID per agent name).
	Engine *durable.Engine

	// DataDir is the durable-go journal directory for an engine this runtime constructs
	// (ignored when Engine is set). Default: "./agent_data/<agent_name>" (sanitized for
	// use as a durable-go taskID), or "./agent_data/default" when the agent has no name.
	//
	// DataDir is exclusive-locked per OS process (in-process occupancy plus an OS flock) —
	// running more than one process/replica against the same DataDir will fail engine
	// construction for the second one (durable.ErrEngineLocked) or block up to LockTimeout.
	// Multi-replica deployments must set a distinct DataDir per instance (e.g. derived from
	// hostname or pod name).
	DataDir string

	// AutoPurgeAge is how long a Completed/Failed run is kept before automatic deletion.
	// Running and Waiting runs (e.g. a pending approval) are never purged regardless of age.
	// 0 uses the default (7 days). Negative disables auto-purge entirely.
	AutoPurgeAge time.Duration

	// AutoPurgeInterval is how often the purge sweep runs. 0 uses the default (1 hour).
	// Ignored when AutoPurgeAge is negative.
	AutoPurgeInterval time.Duration

	// MaxRetries is the default durable-go task retry count for this engine. 0 (default)
	// means a task body runs once; a step failure still stops task-level retries regardless
	// of this value (durable-go: step failures are not retried at the task level).
	MaxRetries int

	// Timeout bounds one run's total durable-go execution time. 0 means no timeout (the
	// durable-go default) — an un-cancelled run (or a step stuck waiting, e.g. an
	// approval nobody ever answers) can then run unbounded. Set this in production.
	Timeout time.Duration

	// LockTimeout bounds how long engine construction waits to acquire the DataDir OS
	// lock. 0 uses the durable-go default (2s).
	LockTimeout time.Duration
}

// durabilityEnabled reports whether cfg requests durable-go execution. A nil cfg (no
// [WithLocalConfig] call) and a zero-value cfg both default to true.
func (cfg *LocalConfig) durabilityEnabled() bool {
	if cfg == nil || cfg.Durability == nil {
		return true
	}
	return *cfg.Durability
}

// defaultDataDir returns "./agent_data/<sanitized-agent-name>", or "./agent_data/default"
// when agentName sanitizes to empty.
func defaultDataDir(agentName string) string {
	return "./agent_data/" + sanitizeDurableID(agentName)
}

// sanitizeDurableID maps agentName onto the character set durable-go accepts for a
// taskID/runID path segment: no "/", "\\", ":", ".", or "..", non-empty. Anything else
// becomes "-"; an empty result falls back to "default".
func sanitizeDurableID(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
}
