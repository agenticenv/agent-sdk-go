package restate

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
)

const (
	defaultEndpointListenAddress = ":9080"
	defaultEventLogTTL           = 90 * time.Second
)

// Option configures a [RestateRuntime] at construction time.
type Option func(*RestateRuntime)

// ToolsResolver resolves per-run tools at handler entry (same process as the Restate endpoint).
// Used as the multi-pod fallback when tools are not stashed in-process.
type ToolsResolver func(ctx context.Context) ([]interfaces.Tool, error)

// RestateConfig holds Restate ingress and endpoint settings for a [RestateRuntime].
type RestateConfig struct {
	Ingress  IngressConfig
	Endpoint EndpointConfig
	// EventLog controls post-run cleanup of the per-run [AgentEventLog] virtual object.
	EventLog EventLogConfig
}

// EventLogConfig configures when AgentEventLog state is Cleared after a root Run/Stream.
//
// By default (zero value), cleanup is enabled and Clear is scheduled EventLog.TTL after
// the run completes (90s). Set DisableClear for short-lived examples so Restate does
// not retry Clear against a process that has already exited.
type EventLogConfig struct {
	// DisableClear skips scheduling AgentEventLog/Clear after a root run.
	// Default false means cleanup is on.
	DisableClear bool
	// TTL is how long to wait after a root Run/Stream completes before Clear.
	// Used only when DisableClear is false. Zero defaults to 90 seconds.
	TTL time.Duration
}

// IngressConfig specifies how this runtime contacts Restate to submit and manage runs.
type IngressConfig struct {
	// URL is the Restate ingress base URL (e.g. "http://localhost:8080").
	URL string
	// AuthKey is an optional bearer token for authenticated ingress (e.g. Restate Cloud).
	AuthKey string
	// HTTPTimeout is the per-attempt timeout for short ingress RPCs.
	// Zero defaults to 30s. Long-running Attach is not bounded by this field.
	HTTPTimeout time.Duration
	// HTTPMaxAttempts is the retry budget for transient failures (network, 429/5xx).
	// Zero defaults to 3.
	HTTPMaxAttempts int
}

// EndpointConfig specifies where this process serves AgentLoop for Restate to invoke.
type EndpointConfig struct {
	// ListenAddress is the bind address for the SDK endpoint (e.g. ":9080").
	// Empty defaults to ":9080".
	ListenAddress string
	// IdentityPublicKeys are optional Restate request-identity public keys
	// used to verify inbound requests from Restate.
	IdentityPublicKeys []string
	// AdminURL is the Restate admin API base URL (e.g. "http://localhost:9070").
	// When set, NewRestateRuntime registers this endpoint automatically after startup.
	AdminURL string
	// DeploymentURL is the URL Restate uses to call back into this process.
	// Empty defaults to http://127.0.0.1 + ListenAddress port. Override when Restate
	// runs in a container with a different network (e.g. "http://host.docker.internal:9080").
	DeploymentURL string
}

// WithRestateConfig sets the Restate connection and endpoint configuration.
func WithRestateConfig(config *RestateConfig) Option {
	return func(r *RestateRuntime) { r.config = config }
}

// WithLogger sets the logger used by the runtime and its Restate endpoint.
func WithLogger(l logger.Logger) Option {
	return func(r *RestateRuntime) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithAgentSpec sets the agent identity (name, description, system prompt).
func WithAgentSpec(spec runtime.AgentSpec) Option {
	return func(r *RestateRuntime) { r.AgentSpec = spec }
}

// WithAgentConfig sets static agent wiring (LLM client, limits, memory, retrievers, hooks).
func WithAgentConfig(cfg runtime.AgentConfig) Option {
	return func(r *RestateRuntime) { r.AgentConfig = cfg }
}

// WithTracer sets the OpenTelemetry tracer for distributed tracing.
func WithTracer(tracer interfaces.Tracer) Option {
	return func(r *RestateRuntime) { r.Tracer = tracer }
}

// WithMetrics sets the metrics sink.
func WithMetrics(metrics interfaces.Metrics) Option {
	return func(r *RestateRuntime) { r.Metrics = metrics }
}

// WithToolExecutionMode controls whether tools run sequentially or in parallel per iteration.
func WithToolExecutionMode(mode types.AgentToolExecutionMode) Option {
	return func(r *RestateRuntime) { r.ToolExecutionMode = mode }
}

// WithApprovalHandler sets the Run-path approval callback.
// The handler is called synchronously for each tool that requires human approval.
// Stream runs use CUSTOM events + [StreamHandle.Approve] instead.
func WithApprovalHandler(fn types.ApprovalHandler) Option {
	return func(r *RestateRuntime) { r.approvalHandler = fn }
}

// WithToolsResolver sets the fallback tool resolver for multi-pod deployments.
// In single-pod mode, tools passed via [RunRequest.Tools] are stashed in-process and
// loaded at handler entry. WithToolsResolver provides a registry-based fallback for
// pods that receive a Restate invocation without a stash entry (different pod).
func WithToolsResolver(fn ToolsResolver) Option {
	return func(r *RestateRuntime) { r.tools.resolver = fn }
}

// newRuntimeFromOptions applies opts and validates the resulting configuration.
func newRuntimeFromOptions(opts ...Option) (*RestateRuntime, error) {
	r := &RestateRuntime{logger: logger.NoopLogger()}
	for _, opt := range opts {
		opt(r)
	}

	if r.config == nil {
		return nil, fmt.Errorf("restate config is required")
	}
	if err := validateConfig(r.config); err != nil {
		return nil, err
	}
	if r.AgentConfig.LLM.Client == nil {
		return nil, fmt.Errorf("llm client is required")
	}
	if r.Tracer == nil {
		r.Tracer = observability.DefaultNoopTracer
	}
	if r.Metrics == nil {
		r.Metrics = observability.DefaultNoopMetrics
	}

	agentName := strings.TrimSpace(r.AgentSpec.Name)
	if agentName == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	r.agentLoopServiceName = serviceName(agentLoopServiceName, agentName)
	r.eventLogServiceName = serviceName(agentEventLogServiceName, agentName)

	r.logger.Debug(context.Background(), "runtime config resolved",
		slog.String("scope", "runtime"),
		slog.String("agentName", r.AgentSpec.Name),
		slog.String("agentLoopName", r.agentLoopServiceName),
		slog.String("eventLogName", r.eventLogServiceName),
		slog.String("ingressURL", r.config.Ingress.URL),
		slog.String("endpointListenAddress", r.config.Endpoint.ListenAddress),
		slog.Bool("hasIngressAuthKey", r.config.Ingress.AuthKey != ""),
		slog.Duration("ingressHTTPTimeout", r.config.Ingress.HTTPTimeout),
		slog.Int("ingressHTTPMaxAttempts", r.config.Ingress.HTTPMaxAttempts),
		slog.Bool("eventLogDisableClear", r.config.EventLog.DisableClear),
		slog.Duration("eventLogTTL", r.config.EventLog.TTL),
		slog.Int("identityPublicKeyCount", len(r.config.Endpoint.IdentityPublicKeys)),
	)
	return r, nil
}

// serviceName returns base_agentName (e.g. AgentLoop_math-agent).
func serviceName(base, agentName string) string {
	return base + "_" + agentName
}

// validateConfig normalises and validates a RestateConfig, applying defaults for zero fields.
func validateConfig(cfg *RestateConfig) error {
	cfg.Ingress.URL = strings.TrimSpace(cfg.Ingress.URL)
	cfg.Ingress.AuthKey = strings.TrimSpace(cfg.Ingress.AuthKey)
	cfg.Endpoint.ListenAddress = strings.TrimSpace(cfg.Endpoint.ListenAddress)
	cfg.Endpoint.AdminURL = strings.TrimSpace(cfg.Endpoint.AdminURL)
	cfg.Endpoint.DeploymentURL = strings.TrimSpace(cfg.Endpoint.DeploymentURL)

	if cfg.Ingress.URL == "" {
		return fmt.Errorf("restate: Ingress.URL is required")
	}
	u, err := url.Parse(cfg.Ingress.URL)
	if err != nil {
		return fmt.Errorf("restate: Ingress.URL is invalid: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("restate: Ingress.URL must use http or https scheme")
	}
	if u.Host == "" {
		return fmt.Errorf("restate: Ingress.URL must include a host")
	}

	if cfg.Endpoint.ListenAddress == "" {
		cfg.Endpoint.ListenAddress = defaultEndpointListenAddress
	}
	if cfg.Ingress.HTTPTimeout < 0 {
		return fmt.Errorf("restate: Ingress.HTTPTimeout must be >= 0")
	}
	if cfg.Ingress.HTTPTimeout == 0 {
		cfg.Ingress.HTTPTimeout = defaultIngressHTTPTimeout
	}
	if cfg.Ingress.HTTPMaxAttempts < 0 {
		return fmt.Errorf("restate: Ingress.HTTPMaxAttempts must be >= 0")
	}
	if cfg.Ingress.HTTPMaxAttempts == 0 {
		cfg.Ingress.HTTPMaxAttempts = defaultIngressHTTPAttempts
	}
	if cfg.EventLog.TTL < 0 {
		return fmt.Errorf("restate: EventLog.TTL must be >= 0")
	}
	if !cfg.EventLog.DisableClear && cfg.EventLog.TTL == 0 {
		cfg.EventLog.TTL = defaultEventLogTTL
	}

	keys := make([]string, 0, len(cfg.Endpoint.IdentityPublicKeys))
	for _, k := range cfg.Endpoint.IdentityPublicKeys {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	cfg.Endpoint.IdentityPublicKeys = keys
	return nil
}
