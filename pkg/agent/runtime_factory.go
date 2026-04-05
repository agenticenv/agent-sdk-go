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
// Extend with additional branches when new Runtime implementations are added.
func (cfg *agentConfig) buildAgentRuntime(remoteWorker bool) (runtime.Runtime, error) {
	if cfg.hasTemporalRuntime() {
		return cfg.buildTemporalRuntime(remoteWorker)
	}
	return nil, fmt.Errorf("no runtime configured: use WithTemporalConfig or WithTemporalClient")
}

func (cfg *agentConfig) buildTemporalRuntime(remoteWorker bool) (runtime.Runtime, error) {
	options := []temporal.Option{
		temporal.WithLogger(cfg.logger),
		temporal.WithLLMClient(cfg.LLMClient),
		temporal.WithLLMSampling(cfg.llmSampling),
		temporal.WithTools(cfg.toolsList()...),
		temporal.WithSystemPrompt(cfg.SystemPrompt),
		temporal.WithResponseFormat(cfg.responseFormatForLLM()),
		temporal.WithConversation(cfg.conversation),
		temporal.WithConversationSize(cfg.conversationSize),
		temporal.WithToolApprovalPolicy(cfg.toolApprovalPolicy),
		temporal.WithTimeout(cfg.timeout),
		temporal.WithAgentName(cfg.Name),
		temporal.WithMaxIterations(cfg.maxIterations),
		temporal.WithApprovalTimeout(cfg.approvalTimeout),
		temporal.WithRemoteWorker(remoteWorker),
	}
	if cfg.temporalConfig != nil {
		options = append(options, temporal.WithTemporalConfig(cfg.temporalConfig))
	} else {
		options = append(options, temporal.WithTemporalClient(cfg.temporalClient, cfg.taskQueue))
	}
	return temporal.NewTemporalRuntime(options...)
}
