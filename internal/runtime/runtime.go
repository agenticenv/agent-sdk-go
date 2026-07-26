// Package runtime defines internal execution contracts for agent backends (Temporal, in-process, etc.).
// SDK users do not import this package; pkg/agent wires implementations.
// If a file also imports the standard library "runtime" package, alias one import (e.g. agentrt ".../internal/runtime").
package runtime

import (
	"context"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/hooks"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
)

//go:generate mockgen -destination=./mocks/mock_runtime.go -package=mocks github.com/agenticenv/agent-sdk-go/internal/runtime Runtime,RunHandle,StreamHandle
//go:generate mockgen -destination=./mocks/mock_worker_runtime.go -package=mocks github.com/agenticenv/agent-sdk-go/internal/runtime WorkerRuntime

// Runtime runs agent workloads against a backend.
//
// Per-run lifecycle (status, cancel, wait-for-result, event subscription) lives on the
// handles returned by [Runtime.Run] / [Runtime.GetRunHandle] and
// [Runtime.Stream] / [Runtime.GetStreamHandle] — not on Runtime itself.
// Runtime starts or reconnects to runs, resolves out-of-band approvals, and releases
// shared resources.
type Runtime interface {
	// Run starts one blocking-style agent run and returns a [RunHandle] immediately.
	// The run continues asynchronously; use [RunHandle.Get] or [RunHandle.Done] to wait for
	// completion, and [RunHandle.Status]/[RunHandle.Cancel] for lifecycle control.
	// The runtime mints the run ID exposed by [RunHandle.ID]. When using conversation, set
	// [RunRequest.ConversationID]; agent and worker must use the same ID. Agent identity
	// lives on the runtime [AgentSpec] configured at construction. The agent package supplies
	// approval via [RunRequest] when needed.
	Run(ctx context.Context, req *RunRequest) (RunHandle, error)

	// GetRunHandle returns a [RunHandle] for an existing run identified by runID
	// (e.g. after a process restart). Use this instead of [Runtime.Run] when the run
	// was already started. Live same-process reuse is handled at the agent layer
	// (run registry); this method is for runtime-level reconnect.
	// Returns [ErrRunNotFound] when runID is unknown or the runtime cannot recover
	// a handle (e.g. LocalRuntime has no durable run tracking).
	// Returns [ErrRunAlreadyCompleted] when the run has already finished.
	GetRunHandle(ctx context.Context, runID string) (RunHandle, error)

	// Stream starts one streaming agent run and returns a [StreamHandle] immediately.
	// It does not subscribe to events; call [StreamHandle.Events] on the returned handle.
	// The runtime mints the run ID exposed by [StreamHandle.ID]. For approvals (tool or
	// delegation), subscribe via [StreamHandle.Events] and handle CUSTOM events. When using
	// conversation, set [RunRequest.ConversationID] on the request.
	Stream(ctx context.Context, req *RunRequest) (StreamHandle, error)

	// GetStreamHandle returns a [StreamHandle] for an existing streaming run identified by
	// runID (e.g. after a process restart). Use this instead of [Runtime.Stream]
	// when the run was already started; then call [StreamHandle.Events] (optionally with
	// a non-zero fromOffset) to subscribe. Live same-process reuse is handled at the
	// agent layer (stream registry); this method is for runtime-level reconnect.
	// Returns [ErrStreamNotFound] when runID is unknown or the runtime cannot recover
	// a stream handle (e.g. LocalRuntime has no durable stream tracking).
	// Returns [ErrRunAlreadyCompleted] when the run has already finished.
	// Returns [ErrStreamOffsetNotSupported] when the runtime cannot replay at the requested offset.
	GetStreamHandle(ctx context.Context, runID string) (StreamHandle, error)

	// Approve completes a pending tool approval when the runtime uses out-of-band approval
	// (e.g. Temporal CompleteActivity). Returns [ErrApprovalAlreadyResolved] when the token
	// was already completed.
	Approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error

	// Close releases runtime resources (connections, workers, background goroutines).
	Close()
}

// RunHandle is the per-run control surface returned by [Runtime.Run] or
// [Runtime.GetRunHandle]. Status, Cancel, Get, and Done operate on this run only —
// callers never pass a runID back into Runtime for those operations.
type RunHandle interface {
	// ID returns the unique run identifier, stable across process restarts when the
	// backend supports reconnect (e.g. Temporal workflow ID).
	ID() string

	// Status returns the current [types.RunStatus] of this run.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Status(ctx context.Context) (types.RunStatus, error)

	// Cancel requests cancellation of this run.
	// Returns [ErrRunAlreadyCompleted] when the run has already finished.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Cancel(ctx context.Context) error

	// Get blocks until the run finishes and returns the result.
	// Cancelling ctx unblocks Get but does NOT cancel the agent run itself —
	// use [RunHandle.Cancel] for that.
	Get(ctx context.Context) (*types.AgentRunResult, error)

	// Done returns a channel that is closed when the run finishes (success or failure).
	// Safe to call multiple times; always returns the same channel.
	Done() <-chan struct{}
}

// StreamHandle is the per-run control surface returned by [Runtime.Stream] or
// [Runtime.GetStreamHandle]. Status, Cancel, Get, Done, and Events operate on this run
// only — callers never pass a runID back into Runtime for those operations.
type StreamHandle interface {
	// ID returns the unique run identifier, stable across process restarts when the
	// backend supports reconnect (e.g. Temporal workflow ID).
	ID() string

	// Status returns the current [types.RunStatus] of this run.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Status(ctx context.Context) (types.RunStatus, error)

	// Cancel requests cancellation of this run.
	// Returns [ErrRunAlreadyCompleted] when the run has already finished.
	// Returns [ErrRunNotFound] when the underlying run is no longer known.
	Cancel(ctx context.Context) error

	// Get blocks until the stream run finishes and returns the result.
	// Cancelling ctx unblocks Get but does NOT cancel the agent run itself —
	// use [StreamHandle.Cancel] for that.
	Get(ctx context.Context) (*types.AgentRunResult, error)

	// Done returns a channel that is closed when the stream run finishes (success or failure).
	// Safe to call multiple times; always returns the same channel.
	Done() <-chan struct{}

	// Events subscribes to this run's event stream starting at fromOffset.
	// fromOffset == 0 means from the beginning of the run (first-time subscriber).
	// fromOffset > 0 resumes after a crash or reconnect (Temporal only; LocalRuntime
	// returns [ErrStreamOffsetNotSupported] for fromOffset > 0).
	// Returns [ErrRunAlreadyCompleted] if the run has already finished.
	// Returns [ErrRunNotFound] if the run is unknown.
	// The channel is closed after the terminal lifecycle event (RUN_FINISHED / RUN_ERROR).
	Events(ctx context.Context, fromOffset int64) (<-chan events.AgentEvent, error)
}

// WorkerRuntime is [Runtime] plus optional in-process task-queue polling (e.g. Temporal worker).
// [AgentWorker] and embedded local workers type-assert [Runtime] to WorkerRuntime for Start/Stop;
// backends that only act as clients implement [Runtime] but not this interface.
type WorkerRuntime interface {
	Runtime
	// Start begins polling; it typically blocks until Stop is called or ctx is cancelled.
	Start(ctx context.Context) error
	// Stop stops polling and releases worker resources.
	Stop()
}

// SubAgentToolParamQuery is the tool/JSON parameter name for the query sent to a sub-agent.
const SubAgentToolParamQuery = "query"

// SubAgentSpec describes one sub-agent in the delegation tree passed from pkg/agent to a runtime.
// The runtime builds its own internal routing structures from this tree; no runtime-specific fields
// are present here. ToolName is the sanitised tool name derived from Name and used as the map key.
type SubAgentSpec struct {
	Name     string  // human-readable agent name
	ToolName string  // tool name used to invoke this sub-agent (key in runtime route maps)
	Runtime  Runtime // the sub-agent's runtime instance
	Children []*SubAgentSpec
	// Tools is the registry-resolved tool list for this sub-agent at request time.
	Tools []interfaces.Tool `json:"-"`
}

// AgentSpec describes agent identity and structured-output preferences configured on the runtime.
type AgentSpec struct {
	// Name is a human-readable label (may include spaces). Runtimes may sanitize it when embedding in workflow IDs.
	Name           string
	Description    string
	SystemPrompt   string
	ResponseFormat *interfaces.ResponseFormat
}

// AgentConfig is static agent wiring on the runtime at construction: LLM client, tool approval policy, session, limits, retriever config, exec overrides, and hooks.
type AgentConfig struct {
	LLM                AgentLLM
	ToolApprovalPolicy interfaces.AgentToolApprovalPolicy
	Retrievers         AgentRetrievers
	Session            AgentSession
	Memory             AgentMemory
	Limits             AgentLimits
	ExecutionConfigs   ExecutionConfigs
	Hooks              []hooks.HookGroup
}

// AgentMemory holds long-term memory configuration for recall and store.
type AgentMemory struct {
	Config *memory.Config
}

// AgentRetrievers holds the retriever instances and mode for prefetch and hybrid RAG.
type AgentRetrievers struct {
	// Retrievers is the list of retriever instances registered with the agent.
	Retrievers []interfaces.Retriever
	// Mode is the retriever mode (agentic, prefetch, hybrid).
	Mode types.RetrieverMode
}

// LLMSampling is the runtime package name for per-run sampling options.
// It aliases [types.LLMSampling] so callers share one shape today; a distinct runtime type may replace this alias if the public runtime API needs different fields later.
type LLMSampling = types.LLMSampling

// AgentLLM is the LLM client and sampling overrides for this run.
type AgentLLM struct {
	Client   interfaces.LLMClient
	Sampling *LLMSampling
}

// AgentSession is conversation storage and how many messages to include in LLM context.
type AgentSession struct {
	Conversation                interfaces.Conversation
	ConversationSize            int
	ConversationSaveOnIteration bool
}

// AgentLimits caps iteration and wall-clock behavior for this run.
type AgentLimits struct {
	MaxIterations   int
	Timeout         time.Duration
	ApprovalTimeout time.Duration
}

// RunRequest carries one run request from Agent to Runtime.
// Run identity is not on the request; the runtime mints it and returns it via the handle's ID().
type RunRequest struct {
	UserPrompt string `json:"user_prompt"`
	// ConversationID is the conversation session for this run. Empty means no conversation.
	// Agent and worker must use the same ID when conversation is enabled.
	ConversationID string `json:"conversation_id,omitempty"`
	// EnableLLMStream requests token-level LLM streaming (Agent.Stream typically sets true;
	// Agent.Run sets false). Distinct from agent-event streaming on [StreamHandle.Events].
	EnableLLMStream bool `json:"enable_llm_stream"`
	// EventTypes filters streamed agent events; empty means default (implementation-defined, often all types).
	EventTypes       []events.AgentEventType `json:"event_types,omitempty"`
	SubAgents        []*SubAgentSpec         `json:"sub_agents,omitempty"`
	MaxSubAgentDepth int                     `json:"max_sub_agent_depth"`

	// Tools is the registry-resolved tool list for this run.
	Tools []interfaces.Tool `json:"-"`
}

// ExecutionPolicy is the resolved runtime shape for one agent loop operation (local [executeWithPolicy] or Temporal activity).
type ExecutionPolicy struct {
	Timeout     time.Duration
	MaxAttempts int
	Retry       RetryPolicy
}

// RetryPolicy controls backoff between retry attempts on a resolved [ExecutionPolicy].
type RetryPolicy struct {
	InitialInterval    time.Duration
	BackoffCoefficient float64
	MaximumInterval    time.Duration
}

// ExecutionPolicies holds resolved policies for every loop operation.
type ExecutionPolicies struct {
	LLM          ExecutionPolicy
	ToolAuth     ExecutionPolicy
	ToolExecute  ExecutionPolicy
	MCP          ExecutionPolicy
	A2A          ExecutionPolicy
	Retriever    ExecutionPolicy
	Memory       ExecutionPolicy
	Conversation ExecutionPolicy
	SubAgent     ExecutionPolicy
}

// ExecutionConfig is a partial override for timeout and retry budget on one agent loop operation.
// Zero Timeout or MaxAttempts mean "use SDK default" at resolve time; see [ResolveExecutionPolicies].
type ExecutionConfig struct {
	Timeout     time.Duration
	MaxAttempts int
}

// ExecutionConfigs holds per-operation execution overrides. Used as agent-level overrides on [AgentConfig]
// and as the SDK default bundle from [defaultExecutionConfigs].
type ExecutionConfigs struct {
	LLM          ExecutionConfig
	ToolAuth     ExecutionConfig
	ToolExecute  ExecutionConfig
	MCP          ExecutionConfig
	A2A          ExecutionConfig
	Retriever    ExecutionConfig
	Memory       ExecutionConfig
	Conversation ExecutionConfig
	SubAgent     ExecutionConfig
}

const (
	defaultLLMExecTimeout          = 30 * time.Minute
	defaultLLMMaxAttempts          = 3
	defaultToolAuthExecTimeout     = 30 * time.Minute
	defaultToolAuthMaxAttempts     = 1
	defaultToolExecuteExecTimeout  = 30 * time.Minute
	defaultToolExecuteMaxAttempts  = 3
	defaultMCPExecTimeout          = 30 * time.Minute
	defaultMCPMaxAttempts          = 3
	defaultA2AExecTimeout          = 30 * time.Minute
	defaultA2AMaxAttempts          = 3
	defaultRetrieverExecTimeout    = 5 * time.Minute
	defaultRetrieverMaxAttempts    = 3
	defaultMemoryExecTimeout       = 5 * time.Minute
	defaultMemoryMaxAttempts       = 3
	defaultConversationExecTimeout = 30 * time.Second
	defaultConversationMaxAttempts = 1
	defaultSubAgentMaxAttempts     = 1

	defaultRetryInitialInterval    = time.Second
	defaultRetryBackoffCoefficient = 2.0
	defaultRetryMaximumInterval    = 10 * time.Minute
)

// defaultExecutionConfigs returns SDK defaults for every loop operation. SubAgent.Timeout is zero;
// runtimes apply the agent run timeout as a fallback when resolving the sub-agent policy.
func defaultExecutionConfigs() ExecutionConfigs {
	return ExecutionConfigs{
		LLM:          ExecutionConfig{Timeout: defaultLLMExecTimeout, MaxAttempts: defaultLLMMaxAttempts},
		ToolAuth:     ExecutionConfig{Timeout: defaultToolAuthExecTimeout, MaxAttempts: defaultToolAuthMaxAttempts},
		ToolExecute:  ExecutionConfig{Timeout: defaultToolExecuteExecTimeout, MaxAttempts: defaultToolExecuteMaxAttempts},
		MCP:          ExecutionConfig{Timeout: defaultMCPExecTimeout, MaxAttempts: defaultMCPMaxAttempts},
		A2A:          ExecutionConfig{Timeout: defaultA2AExecTimeout, MaxAttempts: defaultA2AMaxAttempts},
		Retriever:    ExecutionConfig{Timeout: defaultRetrieverExecTimeout, MaxAttempts: defaultRetrieverMaxAttempts},
		Memory:       ExecutionConfig{Timeout: defaultMemoryExecTimeout, MaxAttempts: defaultMemoryMaxAttempts},
		Conversation: ExecutionConfig{Timeout: defaultConversationExecTimeout, MaxAttempts: defaultConversationMaxAttempts},
		SubAgent:     ExecutionConfig{MaxAttempts: defaultSubAgentMaxAttempts},
	}
}

// resolveExecutionConfig merges an agent override onto an SDK default. Non-zero agent Timeout or
// MaxAttempts replace the corresponding SDK value; zero fields leave the SDK value unchanged.
func resolveExecutionConfig(agent, sdk ExecutionConfig) ExecutionConfig {
	out := sdk
	if agent.Timeout > 0 {
		out.Timeout = agent.Timeout
	}
	if agent.MaxAttempts > 0 {
		out.MaxAttempts = agent.MaxAttempts
	}
	return out
}

// DefaultRetryPolicy returns the SDK backoff defaults applied by [ExecutionConfig.ToPolicy].
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		InitialInterval:    defaultRetryInitialInterval,
		BackoffCoefficient: defaultRetryBackoffCoefficient,
		MaximumInterval:    defaultRetryMaximumInterval,
	}
}

// ToPolicy converts a merged [ExecutionConfig] into a fully populated [ExecutionPolicy].
// MaxAttempts below 1 is clamped to 1 so the operation always runs at least once.
// Backoff is always [DefaultRetryPolicy]; user-facing [ExecutionConfig] does not expose backoff today.
func (c ExecutionConfig) ToPolicy() ExecutionPolicy {
	attempts := c.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	return ExecutionPolicy{
		Timeout:     c.Timeout,
		MaxAttempts: attempts,
		Retry:       DefaultRetryPolicy(),
	}
}

// ResolveExecutionPolicies merges agent per-operation overrides onto SDK defaults and converts
// each operation to a fully populated [ExecutionPolicy]. This is the primary entry point used
// by runtimes at the start of each agent run.
func ResolveExecutionPolicies(execConfigs ExecutionConfigs) ExecutionPolicies {
	defaultConfigs := defaultExecutionConfigs()
	return ExecutionPolicies{
		LLM:          resolveExecutionConfig(execConfigs.LLM, defaultConfigs.LLM).ToPolicy(),
		ToolAuth:     resolveExecutionConfig(execConfigs.ToolAuth, defaultConfigs.ToolAuth).ToPolicy(),
		ToolExecute:  resolveExecutionConfig(execConfigs.ToolExecute, defaultConfigs.ToolExecute).ToPolicy(),
		MCP:          resolveExecutionConfig(execConfigs.MCP, defaultConfigs.MCP).ToPolicy(),
		A2A:          resolveExecutionConfig(execConfigs.A2A, defaultConfigs.A2A).ToPolicy(),
		Retriever:    resolveExecutionConfig(execConfigs.Retriever, defaultConfigs.Retriever).ToPolicy(),
		Memory:       resolveExecutionConfig(execConfigs.Memory, defaultConfigs.Memory).ToPolicy(),
		Conversation: resolveExecutionConfig(execConfigs.Conversation, defaultConfigs.Conversation).ToPolicy(),
		SubAgent:     resolveExecutionConfig(execConfigs.SubAgent, defaultConfigs.SubAgent).ToPolicy(),
	}
}
