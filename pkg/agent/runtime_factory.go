package agent

import (
	"fmt"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/temporal"
)

// hasTemporalRuntime reports whether agent config selects the Temporal backend.
func (cfg *agentConfig) hasTemporalRuntime() bool {
	return cfg.temporalConfig != nil || cfg.temporalClient != nil
}

// buildAgentRuntime constructs the execution backend from agentConfig.
// Extend with additional branches when new [runtime.Runtime] implementations are added.
func (cfg *agentConfig) buildAgentRuntime(remoteWorker bool) (runtime.Runtime, error) {
	if cfg.hasTemporalRuntime() {
		return cfg.buildTemporalRuntime(remoteWorker)
	}
	return nil, fmt.Errorf("no runtime configured: use WithTemporalConfig or WithTemporalClient")
}

func (cfg *agentConfig) buildTemporalRuntime(remoteWorker bool) (runtime.Runtime, error) {
	options := []temporal.Option{
		temporal.WithLogger(cfg.logger),
		temporal.WithAgentSpec(cfg.runtimeAgentSpec()),
		temporal.WithAgentExecution(cfg.runtimeAgentExecution()),
		temporal.WithPolicyFingerprint(toolPolicyFingerprint(cfg.toolApprovalPolicy)),
		temporal.WithRemoteWorker(remoteWorker),
	}
	if cfg.temporalConfig != nil {
		options = append(options, temporal.WithTemporalConfig(cfg.temporalConfig))
	} else {
		options = append(options, temporal.WithTemporalClient(cfg.temporalClient, cfg.taskQueue))
	}
	if cfg.instanceId != "" {
		options = append(options, temporal.WithInstanceId(cfg.instanceId))
	}
	// Event pipeline runs only on the client runtime; always set so worker runtimes get false explicitly.
	enableRemote := !remoteWorker && cfg.enableRemoteWorkers
	options = append(options, temporal.WithEnableRemoteWorkers(enableRemote))
	return temporal.NewTemporalRuntime(options...)
}
