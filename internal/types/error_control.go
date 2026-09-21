package types

import (
	"context"
	"time"
)

// ErrorControlAction is a control-flow decision returned from an AgentErrorHooks callback.
type ErrorControlAction int

const (
	// ErrorControlAbort stops the run and returns the LLM failure (OnLLMFailure default).
	ErrorControlAbort ErrorControlAction = iota
	// ErrorControlFallbackModel switches to FallbackLLMClient for the rest of the run.
	// Config time requires that name in WithNamedLLMClients. Apply time aborts if it is unset.
	ErrorControlFallbackModel
	// ErrorControlContinueWithFinalCall is the max-iterations default: one last LLM call with tools skipped.
	ErrorControlContinueWithFinalCall
	// ErrorControlExtendIterations grants ExtraIterations more rounds on the same run (hard-capped by the SDK).
	ErrorControlExtendIterations
)

// ErrorControlDecision is the hook return value.
type ErrorControlDecision struct {
	Action          ErrorControlAction
	ExtraIterations int // ErrorControlExtendIterations only
}

// LLMFailureInfo is passed to [AgentErrorHooks.OnLLMFailure] after LLM retries are exhausted.
// Err is the last Generate/stream error. Switch with errors.As on *interfaces.LLMError for Reason.
type LLMFailureInfo struct {
	Attempt int // post-policy attempt count
	Err     error
}

// MaxIterationsInfo is passed to [AgentErrorHooks.OnMaxIterationsExceeded] when the last iteration still has tool calls.
type MaxIterationsInfo struct {
	IterationCount int
	MaxIterations  int
	LastAction     string
}

// AgentErrorHooks are optional post-retry control-flow callbacks. Nil field = SDK default.
// Separate from lifecycle hooks. Set via ErrorControlConfig.Hooks.
type AgentErrorHooks struct {
	OnLLMFailure            func(context.Context, LLMFailureInfo) ErrorControlDecision
	OnMaxIterationsExceeded func(context.Context, MaxIterationsInfo) ErrorControlDecision
}

// ErrorControlConfig is hooks, a named fallback LLM client, and circuit breaker.
// FallbackLLMClient is a key in WithNamedLLMClients, not a client.
type ErrorControlConfig struct {
	Hooks             AgentErrorHooks
	FallbackLLMClient string
	CircuitBreaker    *CircuitBreakerConfig
}

// CircuitBreakerConfig trips a per-tool skip after consecutive same-args or alternating-pattern failures.
type CircuitBreakerConfig struct {
	MaxConsecutiveSameArgs int
	// PatternWindowSize is the sliding-window length for A-B-A-B.
	// Even and >= 4 enables it; below 4 disables it. An odd size never trips.
	PatternWindowSize int
	// ResetAfter is how long a tripped tool stays skipped. Zero = remainder of the run.
	ResetAfter time.Duration
}
