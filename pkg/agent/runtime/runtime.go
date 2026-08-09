// Package runtime holds the opt-in execution-runtime factory contract used by
// [github.com/agenticenv/agent-sdk-go/pkg/agent] and the temporal/restate
// subpackages. Local runtime is built directly by pkg/agent and does not use
// RuntimeFactory.
package runtime

//go:generate mockgen -destination=./mocks/mock_runtime_factory.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/agent/runtime RuntimeFactory

import (
	"context"

	internal_runtime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

type RuntimeName string

const (
	RuntimeNameTemporal RuntimeName = "temporal"
	RuntimeNameRestate  RuntimeName = "restate"
)

// RuntimeFactory builds an opt-in agent execution runtime (Temporal, Restate, …).
type RuntimeFactory interface {
	// Name identifies the runtime ("temporal", "restate").
	Name() string
	// Validate checks factory-specific options before the agent is built.
	Validate() error
	// Build constructs the execution runtime.
	Build(params *RuntimeParams, remoteWorker bool) (internal_runtime.Runtime, error)
}

// RuntimeParams is the agent wiring snapshot passed to [RuntimeFactory.Build].
type RuntimeParams struct {
	Logger                   logger.Logger
	AgentSpec                internal_runtime.AgentSpec
	AgentConfig              internal_runtime.AgentConfig
	ApprovalHandler          types.ApprovalHandler
	Tracer                   interfaces.Tracer
	Metrics                  interfaces.Metrics
	ToolExecutionMode        types.AgentToolExecutionMode
	ToolsResolver            func(context.Context) ([]interfaces.Tool, error)
	PolicyFingerprint        string
	MCPFingerprint           string
	A2AFingerprint           string
	ObservabilityFingerprint string
	RetrieverFingerprint     string
	HooksFingerprint         string
	AgentMode                string
	DisableLocalWorker       bool
	DisableFingerprintCheck  bool
}

// RuntimeFactoryOption is an opaque agent option. Concrete type is pkg/agent.Option;
// callers in opt-in packages type-assert when returning to agent.NewAgent.
type RuntimeFactoryOption any

// withRuntimeFactoryHook is installed by pkg/agent on init.
var withRuntimeFactoryHook func(RuntimeFactory) RuntimeFactoryOption

// RegisterWithRuntimeFactoryHook is called once from pkg/agent so opt-in packages
// can attach a [RuntimeFactory] without an exported agent.WithRuntimeFactory.
func RegisterWithRuntimeFactoryHook(h func(RuntimeFactory) RuntimeFactoryOption) {
	withRuntimeFactoryHook = h
}

// WithRuntimeFactory attaches a runtime factory to the agent config.
// Used by pkg/agent/runtime/temporal and pkg/agent/runtime/restate only.
func WithRuntimeFactory(f RuntimeFactory) RuntimeFactoryOption {
	if withRuntimeFactoryHook == nil {
		panic("agent/runtime: pkg/agent not initialized")
	}
	return withRuntimeFactoryHook(f)
}
