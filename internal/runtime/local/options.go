package local

import (
	"context"
	"fmt"
	"log/slog"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
)

type Option func(*LocalRuntime)

func WithLogger(l logger.Logger) Option {
	return func(r *LocalRuntime) {
		if l != nil {
			r.logger = l
		}
	}
}

func WithAgentSpec(spec sdkruntime.AgentSpec) Option {
	return func(r *LocalRuntime) {
		r.AgentSpec = spec
	}
}

func WithAgentConfig(cfg sdkruntime.AgentConfig) Option {
	return func(r *LocalRuntime) {
		r.AgentConfig = cfg
	}
}

func WithTracer(tracer interfaces.Tracer) Option {
	return func(r *LocalRuntime) {
		r.Tracer = tracer
	}
}

func WithMetrics(metrics interfaces.Metrics) Option {
	return func(r *LocalRuntime) {
		r.Metrics = metrics
	}
}

func WithToolExecutionMode(mode types.AgentToolExecutionMode) Option {
	return func(r *LocalRuntime) {
		r.ToolExecutionMode = mode
	}
}

// WithApprovalHandler sets the Run-path approval callback (from agent WithApprovalHandler).
// Stream uses CUSTOM events + Approve instead.
func WithApprovalHandler(fn types.ApprovalHandler) Option {
	return func(r *LocalRuntime) {
		r.approvalHandler = fn
	}
}

// WithLocalConfig sets durable-go execution configuration. Nil (or never calling this
// option) means the zero-value [LocalConfig] — durable by default. See [LocalConfig] for
// field semantics.
func WithLocalConfig(cfg *LocalConfig) Option {
	return func(r *LocalRuntime) {
		r.localConfig = cfg
	}
}

// ToolsResolver resolves the static tool list for a run with no live per-request Tools —
// used to rehydrate a resumed durable run after a process restart (see
// [LocalRuntime.toolsResolver]). Matches [github.com/agenticenv/agent-sdk-go/pkg/agent/runtime.RuntimeParams.ToolsResolver].
type ToolsResolver func(ctx context.Context) ([]interfaces.Tool, error)

// WithToolsResolver sets the callback [LocalRuntime.GetRunHandle] / [LocalRuntime.GetStreamHandle]
// use to rebuild a resumed durable run's tool list. Optional — nil means a resumed run has no
// tools (LLM calls still work; tool calls the LLM attempts will run with an empty tool list).
func WithToolsResolver(fn ToolsResolver) Option {
	return func(r *LocalRuntime) {
		r.toolsResolver = fn
	}
}

func buildLocalRuntime(opts ...Option) (*LocalRuntime, error) {
	r := &LocalRuntime{logger: logger.NoopLogger()}
	for _, opt := range opts {
		opt(r)
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

	r.logger.Debug(context.Background(), "runtime config resolved",
		slog.String("scope", "runtime"),
		slog.String("agentName", r.AgentSpec.Name),
		slog.Bool("hasTracer", r.Tracer != nil),
		slog.Bool("hasMetrics", r.Metrics != nil),
	)
	return r, nil
}
