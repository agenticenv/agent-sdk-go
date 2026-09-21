package base

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

// EffectiveMaxIterations returns the loop bound used when Limits.MaxIterations is unset.
func EffectiveMaxIterations(configured int) int {
	if configured <= 0 {
		return 10
	}
	return configured
}

// ClampExtraIterations enforces the extend-iterations hard cap: ExtraIterations ≥ 1
// and at most the original max-iter bound (one grant; the caller refuses a second).
func ClampExtraIterations(extra, originalMax int) int {
	if extra < 1 {
		return 0
	}
	capAt := EffectiveMaxIterations(originalMax)
	if extra > capAt {
		return capAt
	}
	return extra
}

// LastToolAction is the MaxIterationsInfo.LastAction value: tool names from the last LLM round.
func LastToolAction(toolNames []string) string {
	return strings.Join(toolNames, ",")
}

// LLMPolicyAttempts is the post-policy attempt count reported to OnLLMFailure.
func LLMPolicyAttempts(maxAttempts int) int {
	if maxAttempts < 1 {
		return 1
	}
	return maxAttempts
}

// ResolveLLMClient returns the primary client, or the named fallback when useFallback is true.
func (rt *Runtime) ResolveLLMClient(useFallback bool) (interfaces.LLMClient, error) {
	if !useFallback {
		return rt.AgentConfig.LLM.Client, nil
	}
	cfg := rt.AgentConfig.ErrorControl
	if cfg == nil || strings.TrimSpace(cfg.FallbackLLMClient) == "" {
		return nil, fmt.Errorf("error control: FallbackModel requested but FallbackLLMClient is empty")
	}
	c, ok := rt.AgentConfig.NamedLLMClients[cfg.FallbackLLMClient]
	if !ok || c == nil {
		return nil, fmt.Errorf("error control: FallbackLLMClient %q is not in NamedLLMClients", cfg.FallbackLLMClient)
	}
	return c, nil
}

// DecideLLMFailure runs OnLLMFailure or returns Abort. Invalid actions and
// FallbackModel without a resolvable client become Abort.
func (rt *Runtime) DecideLLMFailure(ctx context.Context, info types.LLMFailureInfo, log logger.Logger) types.ErrorControlDecision {
	abort := types.ErrorControlDecision{Action: types.ErrorControlAbort}
	cfg := rt.AgentConfig.ErrorControl
	if cfg == nil || cfg.Hooks.OnLLMFailure == nil {
		return abort
	}
	d := cfg.Hooks.OnLLMFailure(ctx, info)
	switch d.Action {
	case types.ErrorControlAbort:
		return d
	case types.ErrorControlFallbackModel:
		if _, err := rt.ResolveLLMClient(true); err != nil {
			if log != nil {
				log.Warn(ctx, "error control: FallbackModel unavailable, aborting",
					slog.String("scope", "error_control"), slog.Any("error", err))
			}
			return abort
		}
		return d
	default:
		if log != nil {
			log.Warn(ctx, "error control: invalid OnLLMFailure action, aborting",
				slog.String("scope", "error_control"), slog.Int("action", int(d.Action)))
		}
		return abort
	}
}

// DecideMaxIterations runs OnMaxIterationsExceeded or returns ContinueWithFinalCall.
// alreadyExtended (one grant) and invalid / empty ExtraIterations use the default.
func (rt *Runtime) DecideMaxIterations(ctx context.Context, info types.MaxIterationsInfo, alreadyExtended bool, log logger.Logger) types.ErrorControlDecision {
	final := types.ErrorControlDecision{Action: types.ErrorControlContinueWithFinalCall}
	if alreadyExtended {
		return final
	}
	cfg := rt.AgentConfig.ErrorControl
	if cfg == nil || cfg.Hooks.OnMaxIterationsExceeded == nil {
		return final
	}
	d := cfg.Hooks.OnMaxIterationsExceeded(ctx, info)
	switch d.Action {
	case types.ErrorControlContinueWithFinalCall:
		return d
	case types.ErrorControlExtendIterations:
		extra := ClampExtraIterations(d.ExtraIterations, info.MaxIterations)
		if extra < 1 {
			if log != nil {
				log.Warn(ctx, "error control: ExtendIterations missing ExtraIterations, using final call",
					slog.String("scope", "error_control"), slog.Int("extra", d.ExtraIterations))
			}
			return final
		}
		d.ExtraIterations = extra
		return d
	default:
		if log != nil {
			log.Warn(ctx, "error control: invalid OnMaxIterationsExceeded action, using final call",
				slog.String("scope", "error_control"), slog.Int("action", int(d.Action)))
		}
		return final
	}
}
