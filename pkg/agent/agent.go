package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
)

// Agent runs LLM-backed agent execution through the configured execution runtime.
// It holds configuration, that runtime, and optionally an embedded [AgentWorker] for in-process polling.
// Sub-agents share the parent runtime's event bus for delegation and approvals in the same process.
type Agent struct {
	agentConfig
	runtime          runtime.Runtime
	localAgentWorker *AgentWorker                    // run worker; set when workers are embedded
	runs             *store.KV[string, *agentRun]    // map of runID to AgentRun
	streams          *store.KV[string, *agentStream] // map of runID to AgentStream
}

// AgentRunOptions holds per-call options for [Agent.Run].
// A nil pointer is valid and means "no options" (no conversation, default behaviour).
type AgentRunOptions = types.AgentRunOptions

// AgentStreamOptions holds per-call options for [Agent.Stream].
// A nil pointer is valid and means "no options" (LLM token streaming on, no conversation).
type AgentStreamOptions = types.AgentStreamOptions

// ConversationOptions identifies a conversation session for one call.
// ID must be stable across all turns of the same session (e.g. a user or chat ID).
type ConversationOptions = types.ConversationOptions

// AgentRunResult is the structured result returned by [AgentRun.Get] after a run completes.
type AgentRunResult = types.AgentRunResult

// AgentTelemetry is the unified container for operational insights across
// a single agent run, covering run lifecycle, tool calls, and storage operations.
// Token usage is reported separately on AgentRunResult.LLMUsage.
type AgentTelemetry = types.AgentTelemetry

// LLMUsage is the token usage for a single LLM call.
type LLMUsage = types.LLMUsage

// RunTelemetry captures the orchestration lifecycle metrics for a single agent run.
type RunTelemetry = types.RunTelemetry

// ToolTelemetry tracks tool invocation counts and per-tool breakdowns across a single agent run.
type ToolTelemetry = types.ToolTelemetry

// StorageTelemetry tracks RAG retrieval operations (prefetch, agentic, and hybrid searches).
type StorageTelemetry = types.StorageTelemetry

// buildAgent builds an Agent from options. Validates approval handler when tools require approval.
func buildAgent(opts []Option) (*Agent, error) {
	cfg, err := buildAgentConfig(opts)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		agentConfig: *cfg,
		runs:        store.NewKV[string, *agentRun](),
		streams:     store.NewKV[string, *agentStream](),
	}

	rt, err := cfg.buildAgentRuntime(false)
	if err != nil {
		return nil, err
	}
	a.runtime = rt

	// Worker poll loop is only needed for backends that implement WorkerRuntime (e.g. Temporal).
	// LocalRuntime executes in-process via Execute/ExecuteStream; creating a worker for it would
	// log a spurious error because LocalRuntime does not implement WorkerRuntime.
	if !a.disableLocalWorker && cfg.hasTemporalRuntime() {
		a.localAgentWorker = &AgentWorker{agentConfig: *cfg, runtime: rt}
	}

	return a, nil
}

// NewAgent creates an Agent with the given options.
// Background runtime workers (when used) start lazily when [Agent.Stream] runs or when approvals need them.
func NewAgent(opts ...Option) (*Agent, error) {
	a, err := buildAgent(opts)
	if err != nil {
		return nil, err
	}
	a.logger.Info(context.Background(), "agent created", slog.String("scope", "agent"), slog.String("name", a.Name), slog.String("taskQueue", a.taskQueue), slog.Bool("embedWorker", a.localAgentWorker != nil))
	if a.localAgentWorker != nil {
		go func() {
			if err := a.localAgentWorker.Start(context.Background()); err != nil {
				a.logger.Error(context.Background(), "embedded agent worker failed to start", slog.String("scope", "agent"), slog.Any("error", err))
			}
		}()
	}
	return a, nil
}

// Close stops an embedded local worker if present, then closes the runtime (which may terminate runs,
// release remote resources, and close backend connections owned by the runtime, depending on the implementation).
// Only one run can be active per agent.
func (a *Agent) Close() {
	a.logger.Info(context.Background(), "closing agent", slog.String("scope", "agent"), slog.String("name", a.Name))

	ctx := context.Background()
	if a.localAgentWorker != nil {
		a.logger.Debug(ctx, "stopping local agent worker", slog.String("scope", "agent"))
		a.localAgentWorker.Stop()
	}

	a.runtime.Close()

	// Flush OTLP when built via [WithObservabilityConfig] (batched exporters need Shutdown). No-ops for noop.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = a.tracer.Shutdown(shutdownCtx)
	_ = a.metrics.Shutdown(shutdownCtx)
	_ = a.logs.Shutdown(shutdownCtx)
	cancel()

	a.logger.Info(ctx, "agent closed", slog.String("scope", "agent"), slog.String("name", a.Name))
}

// Run starts an agent run and returns an [AgentRun] immediately after pre-flight validation.
// It calls [runtime.Runtime.Run], wraps the runtime handle with [newAgentRun], and returns.
// Use [AgentRun.Get] or [AgentRun.Done] to wait for the result.
//
// Use [WithApprovalHandler] when any registered tool requires approval.
// When using [WithConversation], pass the conversation ID in opts.
func (a *Agent) Run(ctx context.Context, input string, opts *AgentRunOptions) (AgentRun, error) {
	ctx = a.attachMemoryScopeContext(ctx)
	conversationID := conversationIDFromOpts(opts)

	if err := a.validateConversationID(conversationID); err != nil {
		return nil, err
	}
	tools, err := a.resolveTools(ctx)
	if err != nil {
		return nil, err
	}
	if a.hasApprovalTools(tools) && a.approvalHandler == nil {
		return nil, fmt.Errorf("tools require approval but WithApprovalHandler was not set (required for Run)")
	}
	subAgents, err := a.resolveSubAgentSpecs(ctx)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	ctx, sp := a.tracer.StartSpan(ctx, "agent.run",
		interfaces.Attribute{Key: "agent.name", Value: a.Name},
		interfaces.Attribute{Key: "input.length", Value: len(input)},
	)
	defer sp.End()
	a.metrics.IncrementCounter(ctx, types.MetricRunStarted)

	req := a.createRunRequest(input, conversationID, false, tools, subAgents)
	rh, err := a.runtime.Run(ctx, req)
	elapsed := float64(time.Since(start).Milliseconds())
	if err != nil {
		sp.RecordError(err)
		a.metrics.IncrementCounter(ctx, types.MetricRunFailed, interfaces.Attribute{Key: "error", Value: "runtime_run_failed"})
		a.metrics.RecordHistogram(ctx, types.MetricRunDurationMs, elapsed)
		return nil, err
	}
	sp.SetAttribute("run.id", rh.ID())
	a.metrics.RecordHistogram(ctx, types.MetricRunDurationMs, elapsed)
	a.metrics.IncrementCounter(ctx, types.MetricRunCompleted)
	return newAgentRun(rh, a.runs), nil
}

func copyApprovalArgs(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// Stream starts the agent run and returns an [AgentStream] immediately after pre-flight validation.
// It calls [runtime.Runtime.Stream], wraps the runtime handle with [newAgentStream], and returns.
// Call [AgentStream.Events] to subscribe (optionally [WithOffset] after reconnect).
// Persist [AgentStream.ID] before Events when crash-durability matters.
//
// Cancelling ctx cancels the agent run (same idea as [Agent.Run]). Cancelling the context
// passed to [AgentStream.Events] only stops that subscriber — use a separate Events ctx for
// reconnect / "subscriber gone". You can also stop the run with [AgentStream.Cancel] or
// [WithTimeout].
//
// By default, LLM token streaming is enabled (TEXT_MESSAGE_CONTENT events are emitted).
// Set opts.DisableTokenStreaming = true to receive a single complete message instead.
//
// For approvals (tool or delegation), receive [AgentEventTypeCustom] events from the Events channel
// and call [AgentStream.Approve] with the token extracted from the payload.
// When using [WithConversation], pass the conversation ID in opts.
func (a *Agent) Stream(ctx context.Context, input string, opts *AgentStreamOptions) (AgentStream, error) {
	ctx = a.attachMemoryScopeContext(ctx)
	conversationID := conversationIDFromStreamOpts(opts)

	start := time.Now()
	ctx, sp := a.tracer.StartSpan(ctx, "agent.stream",
		interfaces.Attribute{Key: "agent.name", Value: a.Name},
		interfaces.Attribute{Key: "conversation.id", Value: conversationID},
		interfaces.Attribute{Key: "input.length", Value: len(input)},
	)
	defer sp.End()
	a.metrics.IncrementCounter(ctx, types.MetricStreamStarted)

	if err := a.validateConversationID(conversationID); err != nil {
		sp.RecordError(err)
		a.metrics.IncrementCounter(ctx, types.MetricStreamFailed, interfaces.Attribute{Key: "error", Value: "conversation_id_invalid"})
		a.metrics.RecordHistogram(ctx, types.MetricStreamDurationMs, float64(time.Since(start).Milliseconds()))
		return nil, err
	}

	tools, err := a.resolveTools(ctx)
	if err != nil {
		sp.RecordError(err)
		a.metrics.IncrementCounter(ctx, types.MetricStreamFailed, interfaces.Attribute{Key: "error", Value: "tools_list_failed"})
		a.metrics.RecordHistogram(ctx, types.MetricStreamDurationMs, float64(time.Since(start).Milliseconds()))
		return nil, err
	}
	subAgents, err := a.resolveSubAgentSpecs(ctx)
	if err != nil {
		sp.RecordError(err)
		a.metrics.IncrementCounter(ctx, types.MetricStreamFailed, interfaces.Attribute{Key: "error", Value: "build_sub_agent_specs_failed"})
		a.metrics.RecordHistogram(ctx, types.MetricStreamDurationMs, float64(time.Since(start).Milliseconds()))
		return nil, err
	}

	enableLLMStream := opts == nil || !opts.DisableTokenStreaming
	req := a.createRunRequest(input, conversationID, enableLLMStream, tools, subAgents)
	sh, err := a.runtime.Stream(ctx, req)
	elapsed := float64(time.Since(start).Milliseconds())
	if err != nil {
		sp.RecordError(err)
		a.metrics.IncrementCounter(ctx, types.MetricStreamFailed, interfaces.Attribute{Key: "error", Value: "runtime_stream_failed"})
		a.metrics.RecordHistogram(ctx, types.MetricStreamDurationMs, elapsed)
		return nil, err
	}
	sp.SetAttribute("run.id", sh.ID())
	a.metrics.RecordHistogram(ctx, types.MetricStreamDurationMs, elapsed)
	a.metrics.IncrementCounter(ctx, types.MetricStreamDispatched)
	return newAgentStream(sh, a.streams), nil
}

// GetAgentRun returns an [AgentRun] for runID so callers can await or control a run started
// in this or a previous process ([AgentRun.Get], [AgentRun.Done], [AgentRun.Status], [AgentRun.Cancel]).
//
// Same-process: if [Agent.Run] or a prior GetAgentRun already registered a live handle for
// runID, that same [AgentRun] is returned. If that handle's [AgentRun.Done] is already closed,
// the registry entry is cleared and [ErrRunAlreadyCompleted] is returned.
//
// Otherwise reconnects with [runtime.Runtime.GetRunHandle] and wraps the runtime handle as an
// [AgentRun] (registered in the in-process run registry). Cancelling ctx only affects the
// reconnect lookup (status/describe); it does not cancel the agent run — use [AgentRun.Cancel].
//
// Returns [ErrRunAlreadyCompleted] when the runtime reports the run is already terminal —
// no handle is returned; load the outcome from conversation/memory instead of calling Get.
// Returns [ErrRunNotFound] when runID is unknown or the runtime cannot reconnect
// (e.g. LocalRuntime after a crash — no durable run tracking). Other GetRunHandle errors
// are returned as-is.
func (a *Agent) GetAgentRun(ctx context.Context, runID string) (AgentRun, error) {
	ctx, sp := a.tracer.StartSpan(ctx, "agent.run.reconnect",
		interfaces.Attribute{Key: "agent.name", Value: a.Name},
		interfaces.Attribute{Key: "run.id", Value: runID},
	)
	defer sp.End()
	a.metrics.IncrementCounter(ctx, types.MetricRunReconnectStarted)

	if existing, ok := a.runs.Get(runID); ok {
		select {
		case <-existing.Done():
			a.runs.Delete(runID)
			a.metrics.IncrementCounter(ctx, types.MetricRunReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "already_completed"})
			return nil, ErrRunAlreadyCompleted
		default:
			a.metrics.IncrementCounter(ctx, types.MetricRunReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "existing"})
			return existing, nil
		}
	}

	rh, err := a.runtime.GetRunHandle(ctx, runID)
	if err != nil {
		if errors.Is(err, types.ErrRunAlreadyCompleted) {
			a.metrics.IncrementCounter(ctx, types.MetricRunReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "already_completed"})
			return nil, ErrRunAlreadyCompleted
		}
		sp.RecordError(err)
		a.metrics.IncrementCounter(ctx, types.MetricRunReconnectFailed, interfaces.Attribute{Key: "error", Value: "runtime_get_run_handle_failed"})
		return nil, err
	}

	run := newAgentRun(rh, a.runs)
	a.metrics.IncrementCounter(ctx, types.MetricRunReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "live"})
	return run, nil
}

// GetAgentStream returns an [AgentStream] for runID so callers can subscribe or control a
// stream started in this or a previous process ([AgentStream.Events], [AgentStream.Status],
// [AgentStream.Cancel]). Use [WithOffset] on Events to resume after a crash.
//
// Same-process: if [Agent.Stream] or a prior GetAgentStream already registered a live handle
// for runID, that same [AgentStream] is returned. If [AgentStream.Status] reports terminal,
// the registry entry is cleared and [ErrRunAlreadyCompleted] is returned.
//
// Otherwise reconnects with [runtime.Runtime.GetStreamHandle] and wraps the runtime handle as
// an [AgentStream] (registered in the in-process stream registry). Cancelling ctx only affects
// the reconnect lookup (status/describe); it does not cancel the agent run — use
// [AgentStream.Cancel]. Cancelling [AgentStream.Events]'s ctx stops that subscriber only.
//
// Returns [ErrRunAlreadyCompleted] when the runtime reports the stream run is already terminal —
// no handle is returned; load the outcome from conversation/memory instead of calling Events.
// Returns [ErrStreamNotFound] when runID is unknown or the runtime cannot reconnect
// (e.g. LocalRuntime after a crash — no durable stream tracking). Other GetStreamHandle errors
// are returned as-is.
func (a *Agent) GetAgentStream(ctx context.Context, runID string) (AgentStream, error) {
	ctx, sp := a.tracer.StartSpan(ctx, "agent.stream.reconnect",
		interfaces.Attribute{Key: "agent.name", Value: a.Name},
		interfaces.Attribute{Key: "run.id", Value: runID},
	)
	defer sp.End()
	a.metrics.IncrementCounter(ctx, types.MetricStreamReconnectStarted)

	if existing, ok := a.streams.Get(runID); ok {
		select {
		case <-existing.Done():
			a.streams.Delete(runID)
			a.metrics.IncrementCounter(ctx, types.MetricStreamReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "already_completed"})
			return nil, ErrRunAlreadyCompleted
		default:
			a.metrics.IncrementCounter(ctx, types.MetricStreamReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "existing"})
			return existing, nil
		}
	}

	sh, err := a.runtime.GetStreamHandle(ctx, runID)
	if err != nil {
		if errors.Is(err, types.ErrRunAlreadyCompleted) {
			a.metrics.IncrementCounter(ctx, types.MetricStreamReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "already_completed"})
			return nil, ErrRunAlreadyCompleted
		}
		sp.RecordError(err)
		a.metrics.IncrementCounter(ctx, types.MetricStreamReconnectFailed, interfaces.Attribute{Key: "error", Value: "runtime_get_stream_handle_failed"})
		return nil, err
	}

	stream := newAgentStream(sh, a.streams)
	a.metrics.IncrementCounter(ctx, types.MetricStreamReconnectCompleted, interfaces.Attribute{Key: "outcome", Value: "live"})
	return stream, nil
}

func (a *Agent) attachMemoryScopeContext(ctx context.Context) context.Context {
	if a.Name != "" {
		ctx = memory.WithContextAgentID(ctx, a.Name)
	}
	return ctx
}

// conversationIDFromOpts extracts the conversation ID from AgentRunOptions.
func conversationIDFromOpts(opts *AgentRunOptions) string {
	if opts != nil && opts.ConversationOptions != nil {
		return opts.ConversationOptions.ID
	}
	return ""
}

// conversationIDFromStreamOpts extracts the conversation ID from AgentStreamOptions.
func conversationIDFromStreamOpts(opts *AgentStreamOptions) string {
	if opts != nil && opts.ConversationOptions != nil {
		return opts.ConversationOptions.ID
	}
	return ""
}

func (a *Agent) validateConversationID(conversationID string) error {
	if conversationID != "" && a.conversationConfig == nil {
		return fmt.Errorf("conversationID %s requires conversation configuration", conversationID)
	}
	if conversationID == "" && a.conversationConfig != nil {
		return fmt.Errorf("conversationID is required when using conversation")
	}
	return nil
}

// createRunRequest builds a [runtime.RunRequest] for [Agent.Run] / [Agent.Stream].
// The runtime mints the run ID; conversationID and enableLLMStream come from call options.
func (a *Agent) createRunRequest(
	userPrompt, conversationID string,
	enableLLMStream bool,
	tools []interfaces.Tool,
	subAgents []*runtime.SubAgentSpec,
) *runtime.RunRequest {
	return &runtime.RunRequest{
		UserPrompt:       userPrompt,
		ConversationID:   conversationID,
		EnableLLMStream:  enableLLMStream,
		SubAgents:        subAgents,
		MaxSubAgentDepth: a.maxSubAgentDepth,
		Tools:            tools,
	}
}

// resolveSubAgentSpecs builds the runtime-agnostic sub-agent spec tree for this agent.
// Each runtime receives this tree via ExecuteRequest.SubAgents and constructs its own
// internal routing structures (local: *LocalRuntime refs; temporal: task queue + fingerprint).
func (a *Agent) resolveSubAgentSpecs(ctx context.Context) ([]*runtime.SubAgentSpec, error) {
	if a == nil || a.subAgentRegistry == nil {
		return nil, nil
	}
	subs := a.subAgentRegistry.List()
	if len(subs) == 0 {
		return nil, nil
	}
	out := make([]*runtime.SubAgentSpec, 0, len(subs))
	for _, sub := range subs {
		if sub == nil {
			continue
		}
		toolName, err := subAgentToolName(sub.Name)
		if err != nil || toolName == "" {
			continue
		}
		tools, err := sub.resolveTools(ctx)
		if err != nil {
			return nil, err
		}
		children, err := sub.resolveSubAgentSpecs(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, &runtime.SubAgentSpec{
			Name:     sub.Name,
			ToolName: toolName,
			Runtime:  sub.runtime,
			Children: children,
			Tools:    tools,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	if a.logger != nil {
		names := make([]string, 0, len(out))
		for _, s := range out {
			names = append(names, s.ToolName)
		}
		sort.Strings(names)
		a.logger.Debug(context.Background(), "built sub-agent specs for runtime delegation",
			slog.String("scope", "agent"),
			slog.Any("subAgentToolNames", names),
			slog.Int("specCount", len(out)))
	}
	return out, nil
}

// ToolRegistry returns the agent's tool registry.
func (a *Agent) ToolRegistry() ToolRegistry {
	if a == nil {
		return nil
	}
	return a.toolRegistry
}

// MCPRegistry returns the agent's MCP client registry.
func (a *Agent) MCPRegistry() MCPRegistry {
	if a == nil {
		return nil
	}
	return a.mcpRegistry
}

// A2ARegistry returns the agent's A2A client registry.
func (a *Agent) A2ARegistry() A2ARegistry {
	if a == nil {
		return nil
	}
	return a.a2aRegistry
}

// SubAgentRegistry returns the agent's sub-agent registry.
func (a *Agent) SubAgentRegistry() SubAgentRegistry {
	if a == nil {
		return nil
	}
	return a.subAgentRegistry
}
