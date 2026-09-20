// Package local provides durable-go configuration for the local (in-process) execution
// runtime used by [agent.NewAgent] when no opt-in runtime factory (Temporal, Restate) is
// configured.
//
// Local runtime is durable by default: every Run/Stream call is journaled via durable-go so
// an in-process crash can be resumed instead of losing the in-flight agent turn. Import this
// package and use [WithLocalConfig] to customize that behavior (change the journal
// directory, tune retries/timeouts) or opt out entirely by setting [LocalConfig.Durability]
// to a pointer to false — see [DurabilityOff].
//
// Same split as Temporal: LocalConfig knobs build a default engine (like
// temporal.WithTemporalConfig). [LocalConfig.Engine] is a caller-owned engine for payload
// codec, journal MAC, step-token key, and other durable-go options (like
// temporal.WithTemporalClient). The agent does not Close a supplied Engine.
package local

import (
	internal_local "github.com/agenticenv/agent-sdk-go/internal/runtime/local"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
)

// LocalConfig configures durable-go-backed execution for the local runtime. See the field
// docs on [github.com/agenticenv/agent-sdk-go/internal/runtime/local.LocalConfig] (aliased
// here) for full semantics: Durability, Engine (caller-owned; TLS/Cloud equivalent),
// DataDir, AutoPurgeAge, AutoPurgeInterval, AutoPurgeMaxRuns, AutoPurgeMaxBytes,
// MaxRetries, Timeout, LockTimeout.
type LocalConfig = internal_local.LocalConfig

// WithLocalConfig sets durable-go execution configuration for the local runtime. Not calling
// this option (or passing nil) means the zero-value LocalConfig — durable by default, using
// an engine constructed from defaults (DataDir "./agent_data/<agent_name>", 7-day auto-purge,
// no run timeout). Set [LocalConfig.Engine] to supply a pre-built engine (payload codec,
// journal MAC, step-token key); the caller owns Close. Incompatible with
// [agent.WithTemporalConfig] / an opt-in runtime factory: those runtimes don't use
// LocalRuntime, so a local config would be silently ignored — the agent build fails
// instead with a clear error.
func WithLocalConfig(cfg *LocalConfig) agent.Option {
	return agentruntime.WithLocalConfig(cfg).(agent.Option)
}

// DurabilityOff returns a pointer to false, for [LocalConfig.Durability]. Opts a local
// runtime out of durable-go entirely, restoring the original pure in-memory execution path
// (no journal, no resume, no crash recovery — matches pre-durability behavior).
func DurabilityOff() *bool {
	off := false
	return &off
}
