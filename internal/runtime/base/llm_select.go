package base

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// llmSelectAbort marks a selector failure that must stop the run.
// Error-control fallback does not apply: a routing failure must not send the
// request to another client.
type llmSelectAbort struct{ error }

func (e llmSelectAbort) Unwrap() error { return e.error }

// IsLLMSelectAbort reports whether err came from model selection
// (selector error, panic, or an unknown client name).
func IsLLMSelectAbort(err error) bool {
	var abort llmSelectAbort
	return errors.As(err, &abort)
}

func abortLLMSelect(err error) error {
	if err == nil || IsLLMSelectAbort(err) {
		return err
	}
	return llmSelectAbort{err}
}

// LLMCallSelection is the client choice carried across LLM calls in one run.
// Pinned with an empty Name keeps the primary client and does not call the selector again.
// Current is the name to report as the previous client. Empty means primary.
type LLMCallSelection struct {
	Pinned  bool
	Name    string
	Current string
}

// NewLLMCallSelection pins a per-run client name. An empty name leaves the choice open.
func NewLLMCallSelection(perRunName string) LLMCallSelection {
	if perRunName == "" {
		return LLMCallSelection{}
	}
	return LLMCallSelection{Pinned: true, Name: perRunName, Current: perRunName}
}

// RestoreLLMCallSelection rebuilds the choice after a durable continue.
// A non-empty per-run name wins. Otherwise a saved pin is restored, including a
// pin to the primary (pinned with an empty name). An unpinned saved name is the
// previous client for a per-call selector.
func RestoreLLMCallSelection(perRunName string, pinned bool, storedName string) LLMCallSelection {
	if perRunName != "" {
		return NewLLMCallSelection(perRunName)
	}
	if pinned {
		return LLMCallSelection{Pinned: true, Name: storedName, Current: storedName}
	}
	return LLMCallSelection{Current: storedName}
}

// Apply copies the choice onto one LLM call. usage is cloned.
func (s LLMCallSelection) Apply(in *ExecuteLLMInput, depth int, usage *interfaces.LLMUsage, finalCall bool) {
	if in == nil {
		return
	}
	in.LLMSelectionPinned = s.Pinned
	in.LLMClientName = s.Name
	in.CurrentLLMClient = s.Current
	in.SubAgentDepth = depth
	in.RunUsage = CloneLLMUsage(usage)
	in.FinalCall = finalCall
}

// After records the client that answered and pins it when the selector is per-run.
// A per-run override already pinned in [NewLLMCallSelection] is left as-is.
func (s LLMCallSelection) After(cfg *sdkruntime.LLMSelectorConfig, perRunName, resultName string) LLMCallSelection {
	next := s
	next.Current = resultName
	if perRunName != "" || next.Pinned {
		next.Pinned = true
		if perRunName != "" {
			next.Name = perRunName
		}
		return next
	}
	if cfg != nil && cfg.Mode != sdkruntime.LLMSelectPerCall {
		next.Pinned = true
		next.Name = resultName
	}
	return next
}

// StoredName is the name to keep across a durable continue.
// When the choice is pinned, the name is the pin (empty means primary).
// Otherwise it is the previous client.
func (s LLMCallSelection) StoredName() (pinned bool, name string) {
	if s.Pinned {
		return true, s.Name
	}
	return false, s.Current
}

// LLMSelectionActive reports whether this run should count calls by client.
// Fallback alone does not. A selector or a per-run client name does.
func LLMSelectionActive(perRunName string, cfg *sdkruntime.LLMSelectorConfig) bool {
	return perRunName != "" || cfg != nil
}

// NoteLLMClientCall counts one answered call under name when active.
// An empty name is the primary client.
func NoteLLMClientCall(tel *types.AgentTelemetry, active bool, name string) {
	if !active || tel == nil {
		return
	}
	if tel.Run.LLMCallsByClient == nil {
		tel.Run.LLMCallsByClient = make(map[string]int64)
	}
	tel.Run.LLMCallsByClient[name]++
}

// resolveLLMClientForCall chooses the client for one LLM call.
// An explicit input.Client (error-control fallback) is returned as-is.
// A pin skips the selector. Otherwise a configured selector runs.
// The untouched path — no explicit client, no pin, no selector — returns the primary
// client and does not call a selector.
func (rt *Runtime) resolveLLMClientForCall(ctx context.Context, input ExecuteLLMInput, req *interfaces.LLMRequest) (interfaces.LLMClient, string, error) {
	if input.Client != nil {
		name := ""
		if rt.AgentConfig.ErrorControl != nil {
			name = rt.AgentConfig.ErrorControl.FallbackLLMClient
		}
		rt.logLLMClient(ctx, input, name)
		return input.Client, name, nil
	}
	if !input.LLMSelectionPinned && rt.AgentConfig.LLMSelector == nil {
		return rt.AgentConfig.LLM.Client, "", nil
	}
	if input.LLMSelectionPinned {
		client, err := rt.namedOrPrimary(input.LLMClientName)
		if err != nil {
			return nil, "", err
		}
		rt.logLLMClient(ctx, input, input.LLMClientName)
		return client, input.LLMClientName, nil
	}

	name, err := rt.callLLMSelector(ctx, input, req)
	if err != nil {
		return nil, "", err
	}
	client, err := rt.namedOrPrimary(name)
	if err != nil {
		return nil, "", err
	}
	rt.logLLMClient(ctx, input, name)
	return client, name, nil
}

func (rt *Runtime) namedOrPrimary(name string) (interfaces.LLMClient, error) {
	if name == "" {
		return rt.AgentConfig.LLM.Client, nil
	}
	client, ok := rt.AgentConfig.NamedLLMClients[name]
	if !ok || client == nil {
		return nil, abortLLMSelect(fmt.Errorf("llm select: client %q is not in WithNamedLLMClients", name))
	}
	return client, nil
}

func (rt *Runtime) callLLMSelector(ctx context.Context, input ExecuteLLMInput, req *interfaces.LLMRequest) (name string, err error) {
	cfg := rt.AgentConfig.LLMSelector
	if cfg == nil || cfg.Select == nil {
		return "", nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			name = ""
			err = abortLLMSelect(fmt.Errorf("llm select: selector panicked: %v", rec))
		}
	}()

	info := sdkruntime.LLMSelectInfo{
		AgentName:     input.AgentName,
		RunID:         input.RunID,
		Iteration:     input.Iteration,
		FinalCall:     input.FinalCall,
		SubAgentDepth: input.SubAgentDepth,
		Usage:         CloneLLMUsage(input.RunUsage),
		Current:       input.CurrentLLMClient,
	}
	if req != nil {
		info.Request = *req
	}
	name, err = cfg.Select(ctx, info)
	if err != nil {
		return "", abortLLMSelect(fmt.Errorf("llm select: %w", err))
	}
	return name, nil
}

func (rt *Runtime) logLLMClient(ctx context.Context, input ExecuteLLMInput, name string) {
	if input.Logger == nil {
		return
	}
	input.Logger.Debug(ctx, "runtime: LLM client selected",
		slog.String("scope", "runtime"),
		slog.String("llm_client", name),
		slog.Bool("pinned", input.LLMSelectionPinned),
		slog.Bool("final_call", input.FinalCall))
}
