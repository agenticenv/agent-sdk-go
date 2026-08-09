package temporal

import (
	"errors"
	"fmt"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
	"go.temporal.io/sdk/client"
)

// RuntimeFactory implements [agentruntime.RuntimeFactory] for the Temporal backend. It is the
// single place that turns agent-level wiring ([agentruntime.RuntimeParams]) into [TemporalRuntime]
// options, so the Validate/Build logic exists exactly once even though two call sites construct it:
//   - pkg/agent's deprecated WithTemporalConfig / WithTemporalClient (kept working for existing
//     callers; see the deprecation notice on those functions)
//   - the opt-in github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal package, which new
//     code should prefer so local-only apps do not link the Temporal SDK
//
// Exactly one of Config or Client must be set; see [RuntimeFactory.Validate].
type RuntimeFactory struct {
	Config *TemporalConfig
	Client client.Client
	// TaskQueue is required when Client is set. When Config is set instead, the task queue comes
	// from Config.TaskQueue and this field is ignored.
	TaskQueue string
}

var _ agentruntime.RuntimeFactory = (*RuntimeFactory)(nil)

// Name identifies this factory as the "temporal" runtime.
func (f *RuntimeFactory) Name() string { return string(agentruntime.RuntimeNameTemporal) }

// Validate checks that exactly one of Config/Client is set and that the task queue is known.
func (f *RuntimeFactory) Validate() error {
	if f.Config != nil && f.Client != nil {
		return errors.New("provide either WithTemporalConfig or WithTemporalClient, not both")
	}
	if f.Config != nil && f.Config.TaskQueue == "" {
		return errors.New("TaskQueue is required in TemporalConfig: provide a unique name per agent")
	}
	if f.Client != nil && f.TaskQueue == "" {
		return errors.New("taskQueue is required when using WithTemporalClient")
	}
	if f.Config == nil && f.Client == nil {
		return errors.New("temporal runtime requires WithTemporalConfig or WithTemporalClient")
	}
	return nil
}

// Build constructs a [TemporalRuntime] from params, wiring every fingerprint and connection
// option TemporalRuntime supports so caller and worker processes agree on config.
func (f *RuntimeFactory) Build(params *agentruntime.RuntimeParams, remoteWorker bool) (sdkruntime.Runtime, error) {
	if params == nil {
		return nil, fmt.Errorf("temporal: nil RuntimeParams")
	}
	options := []Option{
		WithLogger(params.Logger),
		WithAgentSpec(params.AgentSpec),
		WithAgentConfig(params.AgentConfig),
		WithApprovalHandler(params.ApprovalHandler),
		WithPolicyFingerprint(params.PolicyFingerprint),
		WithMCPFingerprint(params.MCPFingerprint),
		WithA2AFingerprint(params.A2AFingerprint),
		WithObservabilityFingerprint(params.ObservabilityFingerprint),
		WithTracer(params.Tracer),
		WithMetrics(params.Metrics),
		WithAgentMode(params.AgentMode),
		WithToolExecutionMode(params.ToolExecutionMode),
		WithRetrieverFingerprint(params.RetrieverFingerprint),
		WithHooksFingerprint(params.HooksFingerprint),
		WithDisableLocalWorker(params.DisableLocalWorker),
		// Never allow fingerprint bypass on remote worker runtime.
		WithDisableFingerprintCheck(params.DisableFingerprintCheck && !remoteWorker),
		WithRemoteWorker(remoteWorker),
		WithToolsResolver(params.ToolsResolver),
	}
	if f.Config != nil {
		options = append(options, WithTemporalConfig(f.Config))
	} else {
		options = append(options, WithTemporalClient(f.Client, f.TaskQueue))
	}
	return NewTemporalRuntime(options...)
}
