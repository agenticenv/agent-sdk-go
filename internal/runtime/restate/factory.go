package restate

import (
	"errors"
	"fmt"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
)

// RuntimeFactory implements [agentruntime.RuntimeFactory] for the Restate backend. It is the
// single place that turns agent-level wiring ([agentruntime.RuntimeParams]) into [RestateRuntime]
// options. Callers construct it from the opt-in
// github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate package so local-only apps do not
// link the Restate SDK (and wazero).
//
// Config must be set; see [RuntimeFactory.Validate].
type RuntimeFactory struct {
	Config *RestateConfig
}

var _ agentruntime.RuntimeFactory = (*RuntimeFactory)(nil)

// Name identifies this factory as the "restate" runtime.
func (f *RuntimeFactory) Name() string { return string(agentruntime.RuntimeNameRestate) }

// Validate checks that Config is set. Ingress/endpoint field validation runs in
// [NewRestateRuntime] via [validateConfig].
func (f *RuntimeFactory) Validate() error {
	if f.Config == nil {
		return errors.New("restate runtime requires WithRestateConfig")
	}
	return nil
}

// Build constructs a [RestateRuntime] from params. Restate embeds its SDK endpoint in-process,
// so remoteWorker (used by [NewAgentWorker] for Temporal) is rejected.
func (f *RuntimeFactory) Build(params *agentruntime.RuntimeParams, remoteWorker bool) (sdkruntime.Runtime, error) {
	if remoteWorker {
		return nil, errors.New("restate: NewAgentWorker is not used; RestateRuntime embeds the SDK endpoint")
	}
	if params == nil {
		return nil, fmt.Errorf("restate: nil RuntimeParams")
	}
	opts := []Option{
		WithLogger(params.Logger),
		WithAgentSpec(params.AgentSpec),
		WithAgentConfig(params.AgentConfig),
		WithApprovalHandler(params.ApprovalHandler),
		WithTracer(params.Tracer),
		WithMetrics(params.Metrics),
		WithToolExecutionMode(params.ToolExecutionMode),
		WithToolsResolver(params.ToolsResolver),
		WithRestateConfig(f.Config),
	}
	return NewRestateRuntime(opts...)
}
