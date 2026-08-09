package restate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	restatesdk "github.com/restatedev/sdk-go"
	restateingress "github.com/restatedev/sdk-go/ingress"
	restateserver "github.com/restatedev/sdk-go/server"
)

var _ sdkruntime.Runtime = (*RestateRuntime)(nil)

const (
	agentLoopServiceName     = "AgentLoop"
	agentEventLogServiceName = "AgentEventLog"
	agentLoopRunHandler      = "Run"
	agentLoopStreamHandler   = "Stream"
	agentLoopCancelHandler   = "Cancel"
)

// RestateRuntime executes the agent loop via Restate, embedding base.Runtime for shared
// core methods (LLM, tools, memory, retrievers). The Restate SDK endpoint serves
// AgentLoop and AgentEventLog for this agent. Sub-agents are independent Restate agents
// (own listen port / deployment); the parent invokes their AgentLoop service by name.
type RestateRuntime struct {
	base.Runtime

	config               *RestateConfig
	ingressClient        *restateingress.Client
	httpClient           *http.Client
	logger               logger.Logger
	approvalHandler      types.ApprovalHandler
	agentLoopServiceName string // AgentLoop_<agentName>
	eventLogServiceName  string // AgentEventLog_<agentName>

	// tools is the single mechanism for resolving per-run tools.
	// Same-pod runs stash tools at Send time; multi-pod runs fall back to resolver.
	tools toolsProvider

	// endpoint encapsulates the Restate SDK server lifecycle.
	endpoint endpointServer
}

// toolsProvider is the single mechanism replacing the old pendingRuns + resolveToolsFn pair.
// stash holds per-run state (tools, eventTypes, maxDepth) that cannot cross JSON.
// resolver is the multi-pod fallback when no stash entry exists for a run.
type toolsProvider struct {
	stash    sync.Map // runID -> stagedRun
	resolver ToolsResolver
}

// stagedRun is the stash entry populated at Run/Stream time and loaded at handler entry.
type stagedRun struct {
	tools            []interfaces.Tool
	eventTypes       []events.AgentEventType
	maxSubAgentDepth int
}

// endpointServer encapsulates Restate SDK server lifecycle state.
type endpointServer struct {
	mu      sync.Mutex
	server  *restateserver.Restate
	addr    string
	cancel  context.CancelFunc
	started bool
}

// NewRestateRuntime constructs a RestateRuntime, binds AgentLoop and AgentEventLog,
// starts the SDK endpoint, and optionally registers the deployment with the Restate admin API.
func NewRestateRuntime(opts ...Option) (*RestateRuntime, error) {
	r, err := newRuntimeFromOptions(opts...)
	if err != nil {
		return nil, err
	}
	r.logger.Info(context.Background(), "runtime created",
		slog.String("scope", "runtime"),
		slog.String("name", r.AgentSpec.Name),
		slog.String("ingressURL", r.config.Ingress.URL),
		slog.String("endpointListenAddress", r.config.Endpoint.ListenAddress))

	r.httpClient = newIngressHTTPClient(r.Tracer)

	ingressOpts := []restateingress.ClientOption{
		restateingress.WithHttpClient(r.httpClient),
	}
	if key := r.config.Ingress.AuthKey; key != "" {
		ingressOpts = append(ingressOpts, restateingress.WithAuthKey(key))
	}
	r.ingressClient = restateingress.NewClient(r.config.Ingress.URL, ingressOpts...)

	ep := restateserver.NewRestate().
		WithLogger(endpointSlogHandler(r.logger), true).
		Bind(restatesdk.Reflect(AgentLoop{rt: r})).
		Bind(restatesdk.Reflect(AgentEventLog{name: r.eventLogServiceName}, restatesdk.WithEnableLazyState(true)))
	if keys := r.config.Endpoint.IdentityPublicKeys; len(keys) > 0 {
		ep = ep.WithIdentityV1(keys...)
	}
	r.endpoint.server = ep
	r.endpoint.addr = r.config.Endpoint.ListenAddress

	if err := r.startEndpoint(); err != nil {
		return nil, err
	}
	if err := r.registerDeployment(context.Background()); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// Run starts a durable non-streaming agent loop via Restate ingress and returns a RunHandle.
// The run executes asynchronously; use RunHandle.Get or RunHandle.Done to wait for completion.
// When an approvalHandler is configured, a background goroutine drives approvals for this run.
func (rt *RestateRuntime) Run(ctx context.Context, req *sdkruntime.RunRequest) (sdkruntime.RunHandle, error) {
	runID, invocationID, err := rt.sendAgentLoop(ctx, req, agentLoopRunHandler, false)
	if err != nil {
		return nil, err
	}
	handle := newRunHandle(runID, invocationID, rt)
	if rt.approvalHandler != nil {
		go rt.runApprovalLoop(context.Background(), runID, handle)
	}
	return handle, nil
}

// Stream starts a durable streaming agent loop via Restate ingress and returns a StreamHandle.
// Call StreamHandle.Events to subscribe to the event stream.
func (rt *RestateRuntime) Stream(ctx context.Context, req *sdkruntime.RunRequest) (sdkruntime.StreamHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runID, invocationID, err := rt.sendAgentLoop(ctx, req, agentLoopStreamHandler, true)
	if err != nil {
		return nil, err
	}
	return newStreamHandle(runID, invocationID, rt), nil
}

// GetRunHandle reconnects to an existing non-streaming run identified by runID.
// Returns ErrRunNotFound when Restate has no record and ErrRunAlreadyCompleted when finished.
func (rt *RestateRuntime) GetRunHandle(ctx context.Context, runID string) (sdkruntime.RunHandle, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, types.ErrRunNotFound
	}
	if err := rt.ensureIngress(); err != nil {
		return nil, err
	}
	rt.logger.Debug(ctx, "runtime get run handle",
		slog.String("scope", "runtime"), slog.String("runID", runID))
	invocationID, err := rt.resolveRunningInvocation(ctx, agentLoopRunHandler, runID)
	if err != nil {
		return nil, err
	}
	return newRunHandle(runID, invocationID, rt), nil
}

// GetStreamHandle reconnects to an existing streaming run identified by runID.
// Returns ErrStreamNotFound when Restate has no record and ErrRunAlreadyCompleted when finished.
func (rt *RestateRuntime) GetStreamHandle(ctx context.Context, runID string) (sdkruntime.StreamHandle, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, types.ErrStreamNotFound
	}
	if err := rt.ensureIngress(); err != nil {
		return nil, err
	}
	rt.logger.Debug(ctx, "runtime get stream handle",
		slog.String("scope", "runtime"), slog.String("runID", runID))
	invocationID, err := rt.resolveRunningInvocation(ctx, agentLoopStreamHandler, runID)
	if err != nil {
		if errors.Is(err, types.ErrRunNotFound) {
			return nil, types.ErrStreamNotFound
		}
		return nil, err
	}
	return newStreamHandle(runID, invocationID, rt), nil
}

// OnApproval is a deprecated Runtime-interface wrapper around approve.
// Prefer StreamHandle.Approve on the handle returned by Stream or GetStreamHandle.
func (rt *RestateRuntime) OnApproval(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	return rt.approve(ctx, approvalToken, status)
}

// Close stops the Restate SDK endpoint and releases runtime resources.
func (rt *RestateRuntime) Close() {
	rt.endpoint.mu.Lock()
	if rt.endpoint.cancel != nil {
		rt.endpoint.cancel()
		rt.endpoint.cancel = nil
	}
	rt.endpoint.started = false
	rt.endpoint.mu.Unlock()
	rt.logger.Info(context.Background(), "runtime closed",
		slog.String("scope", "runtime"), slog.String("name", rt.AgentSpec.Name))
}

// ensureIngress returns an error when the ingress client is not configured.
func (rt *RestateRuntime) ensureIngress() error {
	if rt.ingressClient == nil {
		return fmt.Errorf("restate: ingress client not configured")
	}
	return nil
}

// executionPolicies resolves agent loop execution policies from the agent config.
func (rt *RestateRuntime) executionPolicies() sdkruntime.ExecutionPolicies {
	return sdkruntime.ResolveExecutionPolicies(rt.AgentConfig.ExecutionConfigs)
}

// toolExecutionPolicy returns the execution policy for a specific tool kind.
func (rt *RestateRuntime) toolExecutionPolicy(kind types.ToolKind, policies sdkruntime.ExecutionPolicies) sdkruntime.ExecutionPolicy {
	switch kind {
	case types.ToolKindMCP:
		return policies.MCP
	case types.ToolKindA2A:
		return policies.A2A
	default:
		return policies.ToolExecute
	}
}

// subAgentExecutionPolicy returns the sub-agent execution policy, falling back to the
// agent run timeout when no explicit sub-agent timeout is configured.
func (rt *RestateRuntime) subAgentExecutionPolicy() sdkruntime.ExecutionPolicy {
	policy := rt.executionPolicies().SubAgent
	if policy.Timeout == 0 && rt.AgentConfig.Limits.Timeout > 0 {
		policy.Timeout = rt.AgentConfig.Limits.Timeout
	}
	return policy
}

// approvalTaskTimeout returns the approval wait duration, capped at types.MaxApprovalTimeout.
func (rt *RestateRuntime) approvalTaskTimeout() time.Duration {
	timeout := rt.AgentConfig.Limits.ApprovalTimeout
	if timeout <= 0 || timeout > types.MaxApprovalTimeout {
		return types.MaxApprovalTimeout
	}
	return timeout
}

// resolveTools returns tools for a run: stash entry first, then resolver fallback, then nil.
func (rt *RestateRuntime) resolveTools(ctx context.Context, runID string) ([]interfaces.Tool, error) {
	if v, ok := rt.tools.stash.Load(runID); ok {
		if entry, _ := v.(stagedRun); len(entry.tools) > 0 {
			return entry.tools, nil
		}
	}
	if rt.tools.resolver != nil {
		return rt.tools.resolver(ctx)
	}
	return nil, nil
}

// loadStagedRun returns the stash entry for runID, or a zero value when not found.
// Does not delete — Restate may replay the handler on the same pod and must find it again.
func (rt *RestateRuntime) loadStagedRun(runID string) stagedRun {
	v, ok := rt.tools.stash.Load(runID)
	if !ok {
		return stagedRun{}
	}
	entry, _ := v.(stagedRun)
	return entry
}

// ingressHTTPTimeout returns the per-attempt timeout for short ingress RPCs.
// Always positive — validateConfig sets defaultIngressHTTPTimeout when zero.
func (rt *RestateRuntime) ingressHTTPTimeout() time.Duration {
	return rt.config.Ingress.HTTPTimeout
}

// eventLogCleanupEnabled reports whether post-run AgentEventLog/Clear is scheduled.
func (rt *RestateRuntime) eventLogCleanupEnabled() bool {
	if rt == nil || rt.config == nil {
		return true
	}
	return !rt.config.EventLog.DisableClear
}

// eventLogTTL returns how long to wait after a root run finishes before
// Clearing AgentEventLog. Always positive when cleanup is enabled — validateConfig
// applies the default when TTL is zero.
func (rt *RestateRuntime) eventLogTTL() time.Duration {
	if rt == nil || rt.config == nil || rt.config.EventLog.TTL <= 0 {
		return defaultEventLogTTL
	}
	return rt.config.EventLog.TTL
}

// ingressHTTPAttempts returns the retry budget for transient ingress failures.
// Always positive — validateConfig sets defaultIngressHTTPAttempts when zero.
func (rt *RestateRuntime) ingressHTTPAttempts() int {
	return rt.config.Ingress.HTTPMaxAttempts
}

// endpointSlogHandler extracts the slog.Handler from an agent logger for the Restate server SDK.
func endpointSlogHandler(l logger.Logger) slog.Handler {
	if l == nil {
		l = logger.NoopLogger()
	}
	if sl, ok := l.(*logger.SlogLogger); ok && sl != nil {
		if sg := sl.Slog(); sg != nil && sg.Handler() != nil {
			return sg.Handler()
		}
	}
	return logger.NoopLogger().(*logger.SlogLogger).Slog().Handler()
}
