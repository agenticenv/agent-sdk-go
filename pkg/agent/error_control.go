package agent

import (
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

// ErrorControlAction is the control-flow choice returned from an [AgentErrorHooks] callback.
type ErrorControlAction = types.ErrorControlAction

const (
	// ErrorControlAbort stops the run and returns the LLM failure. Default for [AgentErrorHooks.OnLLMFailure].
	ErrorControlAbort = types.ErrorControlAbort
	// ErrorControlFallbackModel switches the rest of the run to [ErrorControlConfig.FallbackLLMClient].
	// That name must exist in [WithNamedLLMClients]. If it is empty or missing at apply time, the run aborts.
	ErrorControlFallbackModel = types.ErrorControlFallbackModel
	// ErrorControlContinueWithFinalCall is the max-iterations default: one last LLM call with tools skipped.
	ErrorControlContinueWithFinalCall = types.ErrorControlContinueWithFinalCall
	// ErrorControlExtendIterations adds ExtraIterations more rounds on the same run.
	// The SDK grants this once and caps ExtraIterations at the original [WithMaxIterations] bound.
	ErrorControlExtendIterations = types.ErrorControlExtendIterations
)

// ErrorControlDecision is the value an [AgentErrorHooks] callback returns.
// ExtraIterations is used only with [ErrorControlExtendIterations].
type ErrorControlDecision = types.ErrorControlDecision

// LLMFailureInfo is passed to [AgentErrorHooks.OnLLMFailure] after LLM retries are exhausted.
// Err is typically an [*interfaces.LLMError] — switch with errors.As, not string matching.
type LLMFailureInfo = types.LLMFailureInfo

// MaxIterationsInfo is passed to [AgentErrorHooks.OnMaxIterationsExceeded]
// when the last iteration still has tool calls.
type MaxIterationsInfo = types.MaxIterationsInfo

// AgentErrorHooks are optional post-retry callbacks. A nil field keeps the SDK default.
// Separate from lifecycle [WithHooks]. Set via [ErrorControlConfig.Hooks].
type AgentErrorHooks = types.AgentErrorHooks

// ErrorControlConfig is [WithErrorControl]: hooks, a named fallback LLM, and an optional circuit breaker.
// FallbackLLMClient is a [WithNamedLLMClients] key, not a client value.
type ErrorControlConfig = types.ErrorControlConfig

// CircuitBreakerConfig skips a tool after repeated execute failures (same args or an A-B-A-B pattern).
// The run continues. Threshold 0 disables that detector.
// PatternWindowSize must be even and >= 4 to enable the alternating detector; an odd size never trips.
type CircuitBreakerConfig = types.CircuitBreakerConfig
