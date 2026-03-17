package opts

import (
	"go.temporal.io/sdk/log"

	"github.com/vvsynapse/temporal-agents-go/pkg/agent"
	"github.com/vvsynapse/temporal-agents-go/pkg/interfaces"
)

// Common returns agent options shared by both the agent client and worker.
// Name, description, system prompt, and Temporal config are identical since
// they represent the same agent.
func Common(
	host string,
	port int,
	namespace string,
	taskQueue string,
	llmClient interfaces.LLMClient,
	logger log.Logger,
) []agent.Option {
	return []agent.Option{
		agent.WithName("agent-worker"),
		agent.WithDescription("Agent with remote worker - client and worker run in separate processes"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      host,
			Port:      port,
			Namespace: namespace,
			TaskQueue: taskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(logger),
	}
}
