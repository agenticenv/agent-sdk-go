package agent

import (
	"github.com/agenticenv/agent-sdk-go/internal/runtime/local"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/temporal"
)

// hasTemporalRuntime reports whether agent config selects the Temporal backend.
func (cfg *agentConfig) hasTemporalRuntime() bool {
	return cfg.temporalConfig != nil || cfg.temporalClient != nil
}

func (cfg *agentConfig) buildTemporalRuntime(remoteWorker bool) (*temporal.TemporalRuntime, error) {
	options := []temporal.Option{
		temporal.WithLogger(cfg.logger),
		temporal.WithAgentSpec(cfg.runtimeAgentSpec()),
		temporal.WithAgentConfig(cfg.runtimeAgentConfig()),
		temporal.WithApprovalHandler(cfg.approvalHandler),
		temporal.WithPolicyFingerprint(toolPolicyFingerprint(cfg.toolApprovalPolicy)),
		temporal.WithMCPFingerprint(mcpConfigFingerprint(cfg.mcpServers, mcpExtraClientNames(cfg.mcpClients))),
		temporal.WithA2AFingerprint(a2aConfigFingerprint(cfg.a2aServers, a2aExtraClientNames(cfg.a2aClients))),
		temporal.WithObservabilityFingerprint(observabilityConfigFingerprint(cfg.observabilityConfig)),
		temporal.WithTracer(cfg.tracer),
		temporal.WithMetrics(cfg.metrics),
		temporal.WithAgentMode(string(cfg.agentMode)),
		temporal.WithAgentToolExecutionMode(cfg.agentToolExecutionMode),
		temporal.WithRetrieverFingerprint(retrieverConfigFingerprint(cfg.retrieverMode, cfg.retrievers)),
		temporal.WithHooksFingerprint(hookGroupsFingerprint(cfg.hooks)),
		temporal.WithDisableLocalWorker(cfg.disableLocalWorker),
		// Never allow fingerprint bypass on remote worker runtime.
		temporal.WithDisableFingerprintCheck(cfg.disableFingerprintCheck && !remoteWorker),
		temporal.WithRemoteWorker(remoteWorker),
		temporal.WithToolsResolver(cfg.resolveTools),
	}
	if cfg.temporalConfig != nil {
		options = append(options, temporal.WithTemporalConfig(cfg.temporalConfig))
	} else {
		options = append(options, temporal.WithTemporalClient(cfg.temporalClient, cfg.taskQueue))
	}
	if cfg.instanceId != "" {
		options = append(options, temporal.WithInstanceId(cfg.instanceId))
	}
	return temporal.NewTemporalRuntime(options...)
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
