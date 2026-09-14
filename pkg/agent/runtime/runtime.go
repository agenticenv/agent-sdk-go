// Package runtime holds the opt-in execution-runtime factory contract used by
// [github.com/agenticenv/agent-sdk-go/pkg/agent] and the temporal/restate
// subpackages, plus the hook [pkg/agent/runtime/local] uses to reach pkg/agent's
// unexported agentConfig fields for local's built-in (not opt-in) durability config.
// Local runtime itself is still built directly by pkg/agent and does not implement
// RuntimeFactory.
package runtime

//go:generate mockgen -destination=./mocks/mock_runtime_factory.go -package=mocks github.com/agenticenv/agent-sdk-go/pkg/agent/runtime RuntimeFactory

import (
	"context"

	internal_runtime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	internal_local "github.com/agenticenv/agent-sdk-go/internal/runtime/local"
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

// LocalConfigOption is an opaque agent option. Concrete type is pkg/agent.Option;
// [pkg/agent/runtime/local] type-asserts when returning to agent.NewAgent.
type LocalConfigOption any

// withLocalConfigHook is installed by pkg/agent on init.
var withLocalConfigHook func(*internal_local.LocalConfig) LocalConfigOption

// RegisterWithLocalConfigHook is called once from pkg/agent so
// [github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/local] can attach durable-go
// config for the (always-built-in, non-opt-in) local runtime without an exported
// agent.WithLocalConfig — the same reasoning as [RegisterWithRuntimeFactoryHook], applied
// to local instead of Temporal/Restate.
func RegisterWithLocalConfigHook(h func(*internal_local.LocalConfig) LocalConfigOption) {
	withLocalConfigHook = h
}

// WithLocalConfig attaches durable-go configuration for the local runtime.
// Used by pkg/agent/runtime/local only.
func WithLocalConfig(cfg *internal_local.LocalConfig) LocalConfigOption {
	if withLocalConfigHook == nil {
		panic("agent/runtime: pkg/agent not initialized")
	}
	return withLocalConfigHook(cfg)
}
