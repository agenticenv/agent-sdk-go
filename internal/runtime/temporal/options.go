package temporal

import (
	"context"
	"fmt"
	"log/slog"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/store"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	"go.temporal.io/sdk/client"
)

// Option configures a [TemporalRuntime].
type Option func(*TemporalRuntime)

// WithTemporalConfig dials a new Temporal client from the supplied connection parameters.
// The runtime owns the resulting client and closes it on [TemporalRuntime.Close].
func WithTemporalConfig(config *TemporalConfig) Option {
	return func(rt *TemporalRuntime) {
		rt.temporalConfig = config
		rt.taskQueue = config.TaskQueue
		rt.ownsTemporalClient = true
	}
}

// WithTemporalClient injects a caller-managed Temporal client and task queue.
// The runtime does NOT close this client on [TemporalRuntime.Close].
func WithTemporalClient(tc client.Client, taskQueue string) Option {
	return func(rt *TemporalRuntime) {
		rt.temporalClient = tc
		rt.taskQueue = taskQueue
		rt.ownsTemporalClient = false
	}
}

// WithInstanceId appends a suffix to the task queue (e.g. "myq-pod1") so multiple
// instances of the same agent can run on isolated queues.
func WithInstanceId(instanceId string) Option {
	return func(rt *TemporalRuntime) { rt.instanceId = instanceId }
}

// WithRemoteWorker marks the runtime as a remote worker (true for [NewAgentWorker],
// false for client [Agent] runtimes).
func WithRemoteWorker(remote bool) Option {
	return func(rt *TemporalRuntime) { rt.remoteWorker = remote }
}

// WithLogger sets the [logger.Logger] used by the runtime. A nil value is silently
// ignored so the safe [logger.NoopLogger] default is preserved.
func WithLogger(l logger.Logger) Option {
	return func(rt *TemporalRuntime) {
		if l != nil {
			rt.logger = l
		}
	}
}

// WithAgentSpec sets identity and response format
// (same shape as [sdkruntime.ExecuteRequest.AgentSpec]).
func WithAgentSpec(spec sdkruntime.AgentSpec) Option {
	return func(rt *TemporalRuntime) { rt.AgentSpec = spec }
}

// WithAgentConfig sets static LLM, session, limits, and tool approval policy on the worker runtime.
func WithAgentConfig(cfg sdkruntime.AgentConfig) Option {
	return func(rt *TemporalRuntime) { rt.AgentConfig = cfg }
}

// WithPolicyFingerprint sets the opaque policy digest used with [ComputeAgentFingerprint].
// Must match pkg/agent's toolPolicyFingerprint for the same agent options.
func WithPolicyFingerprint(fp string) Option {
	return func(rt *TemporalRuntime) { rt.policyFingerprint = fp }
}

// WithMCPFingerprint sets the MCP wiring digest used with [ComputeAgentFingerprint].
// Must match pkg/agent's mcpConfigFingerprint for the same WithMCPConfig / WithMCPClients wiring.
func WithMCPFingerprint(fp string) Option {
	return func(rt *TemporalRuntime) { rt.mcpFingerprint = fp }
}

// WithA2AFingerprint sets the A2A wiring digest used with [ComputeAgentFingerprint].
// Must match pkg/agent's a2aConfigFingerprint for the same WithA2AConfig / WithA2AClients wiring.
func WithA2AFingerprint(fp string) Option {
	return func(rt *TemporalRuntime) { rt.a2aFingerprint = fp }
}

// WithObservabilityFingerprint sets the OTLP observability digest used with [ComputeAgentFingerprint].
// Must match pkg/agent observabilityConfigFingerprint for the same WithObservabilityConfig wiring.
func WithObservabilityFingerprint(fp string) Option {
	return func(rt *TemporalRuntime) { rt.observabilityFingerprint = fp }
}

// WithAgentMode sets the agent mode string (e.g. "interactive", "autonomous") used with
// [ComputeAgentFingerprint]. Must match pkg/agent [WithAgentMode] on both caller and worker.
func WithAgentMode(mode string) Option {
	return func(rt *TemporalRuntime) { rt.agentMode = mode }
}

// WithAgentToolExecutionMode sets the tool execution mode. The value is stored in
// the embedded [base.Runtime.ToolExecutionMode] so it drives both fingerprinting and
// workflow-level tool execution.
func WithAgentToolExecutionMode(mode types.AgentToolExecutionMode) Option {
	return func(rt *TemporalRuntime) { rt.ToolExecutionMode = mode }
}

// WithRetrieverFingerprint sets the retriever wiring digest (mode + retriever names).
// Must match pkg/agent [retrieverConfigFingerprint] for the same agent.
func WithRetrieverFingerprint(fp string) Option {
	return func(rt *TemporalRuntime) { rt.retrieverFingerprint = fp }
}

// WithHooksFingerprint sets the hook group names digest used with [ComputeAgentFingerprint].
// Must match pkg/agent [hookGroupsFingerprint] for the same WithHooks wiring.
func WithHooksFingerprint(fp string) Option {
	return func(rt *TemporalRuntime) { rt.hooksFingerprint = fp }
}

// WithToolsResolver sets the callback that resolves tools at activity time on the worker runtime.
func WithToolsResolver(fn ToolsResolver) Option {
	return func(rt *TemporalRuntime) { rt.resolveToolsFn = fn }
}

// WithDisableLocalWorker mirrors pkg/agent DisableLocalWorker. When false, the client
// embeds a worker and the runtime skips DescribeTaskQueue poller checks before starting
// workflows.
func WithDisableLocalWorker(disable bool) Option {
	return func(rt *TemporalRuntime) { rt.disableLocalWorker = disable }
}

// WithDisableFingerprintCheck disables activity-time caller-vs-worker fingerprint
// verification. Break-glass only: use temporarily during rollout incidents; keep false
// in production for safety.
func WithDisableFingerprintCheck(disable bool) Option {
	return func(rt *TemporalRuntime) { rt.disableFingerprintCheck = disable }
}

// WithTracer sets the optional [interfaces.Tracer]. When the runtime dials its own
// Temporal client ([WithTemporalConfig]) and the tracer implements [interfaces.OTelTracer],
// a Temporal OpenTelemetry client interceptor is attached automatically.
func WithTracer(t interfaces.Tracer) Option {
	return func(rt *TemporalRuntime) { rt.Tracer = t }
}

// WithMetrics sets the optional [interfaces.Metrics] for this runtime.
func WithMetrics(m interfaces.Metrics) Option {
	return func(rt *TemporalRuntime) { rt.Metrics = m }
}

// WithApprovalHandler sets the Run-path approval callback (from agent WithApprovalHandler).
// Stream uses CUSTOM events + StreamHandle.Approve instead.
func WithApprovalHandler(fn types.ApprovalHandler) Option {
	return func(rt *TemporalRuntime) { rt.approvalHandler = fn }
}

// buildTemporalRuntime applies options onto a fresh [TemporalRuntime], validates required
// fields, and dials the Temporal client when [WithTemporalConfig] is used. The returned
// runtime is fully configured but does not yet have an eventbus — that is set by [NewTemporalRuntime].
func buildTemporalRuntime(opts ...Option) (*TemporalRuntime, error) {
	rt := &TemporalRuntime{logger: logger.NoopLogger()}
	for _, opt := range opts {
		opt(rt)
	}

	if rt.temporalConfig == nil && rt.temporalClient == nil {
		return nil, fmt.Errorf("temporal config or client is required")
	}

	if rt.temporalConfig != nil {
		tc, err := newTemporalClient(rt.temporalConfig, rt.logger, rt.Tracer)
		if err != nil {
			return nil, err
		}
		rt.temporalClient = tc
	} else { // user-provided Temporal client
		if _, ok := rt.Tracer.(interfaces.OTelTracer); ok {
			rt.logger.Warn(context.Background(),
				"user provided Temporal client — add OTel interceptor manually for tracing",
				slog.String("scope", "runtime"))
		}
	}

	if rt.instanceId != "" {
		rt.taskQueue = rt.taskQueue + "-" + rt.instanceId
	}

	if rt.AgentConfig.LLM.Client == nil {
		return nil, fmt.Errorf("llm client is required")
	}

	if rt.Tracer == nil {
		rt.Tracer = observability.DefaultNoopTracer
	}
	if rt.Metrics == nil {
		rt.Metrics = observability.DefaultNoopMetrics
	}

	if rt.activeRuns == nil {
		rt.activeRuns = store.NewKV[string, *runHandle]()
	}
	if rt.activeStreams == nil {
		rt.activeStreams = store.NewKV[string, *streamHandle]()
	}

	rt.logger.Debug(context.Background(), "runtime config resolved",
		slog.String("scope", "runtime"),
		slog.String("agentName", rt.AgentSpec.Name),
		slog.String("taskQueue", rt.taskQueue),
		slog.String("instanceId", rt.instanceId),
		slog.Int("maxIterations", rt.AgentConfig.Limits.MaxIterations),
		slog.Bool("remoteWorker", rt.remoteWorker),
		slog.String("agentMode", rt.agentMode),
		slog.String("toolExecutionMode", string(rt.ToolExecutionMode)),
		slog.Bool("disableFingerprintCheck", rt.disableFingerprintCheck),
		slog.Duration("timeout", rt.AgentConfig.Limits.Timeout),
		slog.Duration("approvalTimeout", rt.AgentConfig.Limits.ApprovalTimeout),
		slog.Bool("hasConversation", rt.AgentConfig.Session.Conversation != nil),
		slog.Bool("hasTracer", rt.Tracer != nil),
		slog.Bool("hasMetrics", rt.Metrics != nil))

	return rt, nil
}
