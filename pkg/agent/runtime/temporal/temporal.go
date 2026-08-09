// Package temporal provides Temporal options for [agent.NewAgent].
//
// Import this package (and use [WithTemporalConfig] or [WithTemporalClient]) only when
// the app needs Temporal. Prefer this package over the deprecated agent.WithTemporalConfig
// helpers so the intent to link the Temporal SDK is explicit at the import site.
package temporal

import (
	temporal_runtime "github.com/agenticenv/agent-sdk-go/internal/runtime/temporal"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
	"go.temporal.io/sdk/client"
)

// TemporalConfig holds connection settings for the Temporal-based execution runtime.
type TemporalConfig = temporal_runtime.TemporalConfig

// WithTemporalConfig sets connection options for the Temporal execution runtime.
// Use either WithTemporalConfig or WithTemporalClient, not both.
func WithTemporalConfig(cfg *TemporalConfig) agent.Option {
	f := &temporal_runtime.RuntimeFactory{Config: cfg}
	return agentruntime.WithRuntimeFactory(f).(agent.Option)
}

// WithTemporalClient sets a pre-configured client for the Temporal execution runtime.
// Task queue must still be set via taskQueue. Use either WithTemporalConfig or
// WithTemporalClient, not both. The agent does not close the client when Close() is
// called; the caller owns the lifecycle.
func WithTemporalClient(tc client.Client, taskQueue string) agent.Option {
	f := &temporal_runtime.RuntimeFactory{Client: tc, TaskQueue: taskQueue}
	return agentruntime.WithRuntimeFactory(f).(agent.Option)
}
