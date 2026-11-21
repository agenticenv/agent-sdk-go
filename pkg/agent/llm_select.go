package agent

import "github.com/agenticenv/agent-sdk-go/internal/runtime"

// LLMSelectMode chooses how often [LLMSelector] runs during one agent run.
type LLMSelectMode = runtime.LLMSelectMode

const (
	// LLMSelectPerRun runs the selector on the first LLM call and keeps that client
	// for the rest of the run. This is the default.
	LLMSelectPerRun = runtime.LLMSelectPerRun
	// LLMSelectPerCall runs the selector before every LLM call.
	LLMSelectPerCall = runtime.LLMSelectPerCall
)

// LLMSelectInfo is the read-only view passed to an [LLMSelector].
// Request is the request about to be sent, after BeforeLLM hooks. Do not change it.
// Edit the request from a BeforeLLM hook instead.
// Current is the client that answered the previous call. An empty Current is the primary client.
// Usage is this run's token usage so far, not including the call about to be made.
type LLMSelectInfo = runtime.LLMSelectInfo

// LLMSelector returns a [WithNamedLLMClients] name for one LLM call.
// An empty name selects the primary client. A returned error aborts the run.
// The function can run more than once for one call when that call is retried, so keep it fast
// and free of side effects. Context values do not reach a Temporal or Restate worker;
// use the fields on [LLMSelectInfo], or set LLMClient on the run, to pass a choice in.
type LLMSelector = runtime.LLMSelector

// LLMSelectorConfig is [WithLLMSelector].
// Select is required. Mode defaults to [LLMSelectPerRun].
type LLMSelectorConfig = runtime.LLMSelectorConfig
