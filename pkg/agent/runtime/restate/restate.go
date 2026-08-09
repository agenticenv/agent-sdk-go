// Package restate provides Restate options for [agent.NewAgent].
//
// Import this package (and use [WithRestateConfig]) only when the app needs Restate.
// Apps that use the local runtime should not import it, so github.com/restatedev/sdk-go
// (and wazero) stay out of the binary.
package restate

import (
	restate_runtime "github.com/agenticenv/agent-sdk-go/internal/runtime/restate"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
)

// RestateConfig holds connection and endpoint settings for the Restate execution runtime.
//
// Typical usage:
//
//	restate.WithRestateConfig(&restate.RestateConfig{
//		Ingress:  restate.IngressConfig{URL: "http://localhost:8080"},
//		Endpoint: restate.EndpointConfig{ListenAddress: ":9080", AdminURL: "http://localhost:9070"},
//		EventLog: restate.EventLogConfig{}, // cleanup on, 90s TTL (defaults)
//	})
//
// For short-lived examples that exit before delayed Clear can run, disable cleanup:
//
//	EventLog: restate.EventLogConfig{DisableClear: true}
type RestateConfig = restate_runtime.RestateConfig

// IngressConfig is how the agent invokes Restate (Run/Stream/Cancel, awakeables).
type IngressConfig = restate_runtime.IngressConfig

// EndpointConfig is where this process serves AgentLoop for Restate to call into
// (listen address, optional admin registration, deployment callback URL).
type EndpointConfig = restate_runtime.EndpointConfig

// EventLogConfig controls post-run cleanup of the per-run AgentEventLog virtual object.
//
// Zero value enables cleanup with a 90s delay before Clear. Set DisableClear to skip
// Clear entirely (handy for CLI examples). Set TTL to override the delay when cleanup
// is enabled.
type EventLogConfig = restate_runtime.EventLogConfig

// WithRestateConfig selects the Restate execution runtime (ingress + embedded SDK endpoint).
// Mutually exclusive with Temporal options from pkg/agent/runtime/temporal.
func WithRestateConfig(cfg *RestateConfig) agent.Option {
	return agentruntime.WithRuntimeFactory(&restate_runtime.RuntimeFactory{Config: cfg}).(agent.Option)
}
