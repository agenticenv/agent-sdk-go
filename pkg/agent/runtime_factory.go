package agent

import (
	"fmt"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/local"
	temporal_runtime "github.com/agenticenv/agent-sdk-go/internal/runtime/temporal"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
	"go.temporal.io/sdk/client"
)

func init() {
	agentruntime.RegisterWithRuntimeFactoryHook(func(f agentruntime.RuntimeFactory) agentruntime.RuntimeFactoryOption {
		return withRuntimeFactory(f)
	})
}

// TemporalConfig holds connection settings for the Temporal-based execution runtime.
//
// Deprecated: use [github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal.TemporalConfig].
// Agent-level Temporal helpers will be removed in a future release; prefer the opt-in
// pkg/agent/runtime/temporal package so local-only apps do not link the Temporal SDK.
type TemporalConfig = temporal_runtime.TemporalConfig

// WithTemporalConfig sets connection options for the Temporal execution runtime.
// Use either WithTemporalConfig or WithTemporalClient, not both.
//
// Deprecated: use [github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal.WithTemporalConfig].
// This helper is kept working (same behavior, same validation) for existing callers, but it is
// implemented on top of [temporal_runtime.RuntimeFactory] — the same type the opt-in package
// uses — so there is one implementation of the Temporal wiring logic, not two. Because this file
// is part of package agent, calling this function (even indirectly, e.g. transitively through a
// helper package) still links the Temporal SDK into your binary; import the opt-in
// pkg/agent/runtime/temporal package instead if you want local-only builds to skip that
// dependency entirely.
func WithTemporalConfig(cfg *TemporalConfig) Option {
	return withRuntimeFactory(&temporal_runtime.RuntimeFactory{Config: cfg})
}

// WithTemporalClient sets a pre-configured client for the Temporal execution runtime.
// Task queue must still be set via taskQueue. Use either WithTemporalConfig or
// WithTemporalClient, not both. The agent does not close the client when Close() is
// called; the caller owns the lifecycle.
//
// Deprecated: use [github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal.WithTemporalClient].
func WithTemporalClient(tc client.Client, taskQueue string) Option {
	return withRuntimeFactory(&temporal_runtime.RuntimeFactory{Client: tc, TaskQueue: taskQueue})
}

// withRuntimeFactory selects an opt-in execution runtime factory.
// Not exported: opt-in packages call [agentruntime.WithRuntimeFactory] instead.
func withRuntimeFactory(f agentruntime.RuntimeFactory) Option {
	return func(c *agentConfig) {
		if f == nil {
			return
		}
		if c.runtimeFactory != nil && c.runtimeFactory.Name() != f.Name() {
			c.factoryConflict = fmt.Errorf("provide either %s or %s, not both", c.runtimeFactory.Name(), f.Name())
		}
		c.runtimeFactory = f
	}
}

func (cfg *agentConfig) hasTemporalRuntime() bool {
	return cfg.runtimeFactory != nil && cfg.runtimeFactory.Name() == string(agentruntime.RuntimeNameTemporal)
}

func (cfg *agentConfig) hasRestateRuntime() bool {
	return cfg.runtimeFactory != nil && cfg.runtimeFactory.Name() == string(agentruntime.RuntimeNameRestate)
}

func (cfg *agentConfig) runtimeParams() *agentruntime.RuntimeParams {
	return &agentruntime.RuntimeParams{
		Logger:                   cfg.logger,
		AgentSpec:                cfg.runtimeAgentSpec(),
		AgentConfig:              cfg.runtimeAgentConfig(),
		ApprovalHandler:          cfg.approvalHandler,
		Tracer:                   cfg.tracer,
		Metrics:                  cfg.metrics,
		ToolExecutionMode:        cfg.agentToolExecutionMode,
		ToolsResolver:            cfg.resolveTools,
		PolicyFingerprint:        toolPolicyFingerprint(cfg.toolApprovalPolicy),
		MCPFingerprint:           mcpConfigFingerprint(cfg.mcpServers, mcpExtraClientNames(cfg.mcpClients)),
		A2AFingerprint:           a2aConfigFingerprint(cfg.a2aServers, a2aExtraClientNames(cfg.a2aClients)),
		ObservabilityFingerprint: observabilityConfigFingerprint(cfg.observabilityConfig),
		RetrieverFingerprint:     retrieverConfigFingerprint(cfg.retrieverMode, cfg.retrievers),
		HooksFingerprint:         hookGroupsFingerprint(cfg.hooks),
		AgentMode:                string(cfg.agentMode),
		DisableLocalWorker:       cfg.disableLocalWorker,
		DisableFingerprintCheck:  cfg.disableFingerprintCheck,
	}
}

// buildAgentRuntime constructs the execution runtime from agentConfig.
// Defaults to the local in-process runtime when no factory is set.
//
// remoteWorker=true (from [NewAgentWorker]) is only reachable for factories that support it;
// [NewAgentWorker] already rejects non-Temporal factories before calling this, and the Restate
// factory's own Build additionally refuses remoteWorker as defense in depth for callers that
// invoke a [agentruntime.RuntimeFactory] directly.
func (cfg *agentConfig) buildAgentRuntime(remoteWorker bool) (runtime.Runtime, error) {
	if cfg.runtimeFactory != nil {
		return cfg.runtimeFactory.Build(cfg.runtimeParams(), remoteWorker)
	}
	return cfg.buildLocalRuntime()
}

func (cfg *agentConfig) buildLocalRuntime() (*local.LocalRuntime, error) {
	options := []local.Option{
		local.WithLogger(cfg.logger),
		local.WithToolExecutionMode(cfg.agentToolExecutionMode),
		local.WithAgentSpec(cfg.runtimeAgentSpec()),
		local.WithAgentConfig(cfg.runtimeAgentConfig()),
		local.WithApprovalHandler(cfg.approvalHandler),
		local.WithTracer(cfg.tracer),
		local.WithMetrics(cfg.metrics),
	}
	return local.NewLocalRuntime(options...)
}
