package opts

import (
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/currenttime"
)

// Common returns agent options shared by both the agent client and worker.
// The agent and worker must agree on name, description, system prompt, Temporal config,
// and tool set — mismatches cause workflow task failures.
func Common(
	host string,
	port int,
	namespace string,
	taskQueue string,
	llmClient interfaces.LLMClient,
	l logger.Logger,
) []agent.Option {
	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		currenttime.New(),
		calculator.New(),
	); err != nil {
		panic("failed to register tools: " + err.Error())
	}

	return []agent.Option{
		agent.WithName("reconnect-agent"),
		agent.WithDescription("Agent that demonstrates Workflow Streams reconnect"),
		agent.WithSystemPrompt("You are a helpful assistant that can tell the time and do math. Keep responses short."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      host,
			Port:      port,
			Namespace: namespace,
			TaskQueue: taskQueue,
		}),
		agent.WithTimeout(5 * time.Minute),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(l),
	}
}
