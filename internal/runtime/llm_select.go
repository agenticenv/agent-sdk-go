package runtime

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// LLMSelectMode chooses how often a selector runs during one agent run.
type LLMSelectMode int

const (
	// LLMSelectPerRun runs the selector on the first LLM call and keeps that client
	// for the rest of the run. This is the default.
	LLMSelectPerRun LLMSelectMode = iota
	// LLMSelectPerCall runs the selector before every LLM call.
	LLMSelectPerCall
)

// LLMSelectInfo is the read-only view passed to an [LLMSelector].
// Request is the request that will be sent, after BeforeLLM hooks. Do not mutate it.
// Current is the client that answered the previous call in this run. An empty
// Current means the primary client.
type LLMSelectInfo struct {
	AgentName     string
	RunID         string
	Iteration     int
	Request       interfaces.LLMRequest
	FinalCall     bool
	SubAgentDepth int
	Usage         *interfaces.LLMUsage
	Current       string
}

// LLMSelector returns a [WithNamedLLMClients] name. An empty name selects the
// primary client. A returned error aborts the run.
type LLMSelector func(ctx context.Context, info LLMSelectInfo) (string, error)

// LLMSelectorConfig is the agent-level model selector.
// Mode zero is [LLMSelectPerRun].
type LLMSelectorConfig struct {
	Select LLMSelector
	Mode   LLMSelectMode
}
