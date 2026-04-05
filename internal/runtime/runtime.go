// Package runtime defines internal execution contracts for agent backends (Temporal, in-process, etc.).
// SDK users do not import this package; pkg/agent wires implementations.
// If a file also imports the standard library "runtime" package, alias one import (e.g. agentrt ".../internal/runtime").
package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

//go:generate mockgen -destination=./mocks/mock_runtime.go -package=mocks github.com/agenticenv/agent-sdk-go/internal/runtime Runtime

// ErrApprovalNotSupported is returned by Runtime.Approve when the runtime does not use token-based approval.
var ErrApprovalNotSupported = errors.New("runtime: approval not supported")

// Runtime executes agent runs against a backend.
type Runtime interface {
	// Execute runs one execution and returns the result. The agent package supplies approval via ExecuteRequest when needed.
	// Use WithTimeout or a context with deadline to avoid blocking.
	// When using conversation, pass the conversation ID on the request; agent and worker must use the same ID.
	// Agent identity is on req.AgentSpec.Name when AgentSpec is set.
	Execute(ctx context.Context, req *ExecuteRequest) (*types.AgentResponse, error)

	// ExecuteStream starts the run and returns a channel of AgentEvent. Events are streamed until
	// AgentEventComplete from this agent (the root of the run). Complete events from delegated
	// sub-agents are still delivered but do not close the stream. After that root complete, the
	// channel may remain open until the implementation finishes the run (e.g. backend cleanup), then closes.
	// For approvals (tool or delegation), receive AgentEventApproval and call the approval path
	// provided by the agent package (e.g. OnApproval in streaming examples).
	// When using conversation, pass the conversation ID on the request.
	// Agent identity is on req.AgentSpec.Name when AgentSpec is set.
	ExecuteStream(ctx context.Context, req *ExecuteRequest) (chan *types.AgentEvent, error)

	// Approve completes a pending tool approval when the runtime uses out-of-band approval
	// (e.g. Temporal CompleteActivity). Returns ErrApprovalNotSupported if not applicable.
	Approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error

	// Close closes the runtime and releases resources.
	Close()

	// Start begins polling; it typically blocks until Stop is called or ctx is cancelled.
	Start(ctx context.Context) error

	// Stop stops polling and releases worker resources.
	Stop()

	// SetEventBus sets the event bus for the runtime.
	SetEventBus(eventbus eventbus.EventBus)
	// GetEventBus returns the event bus for the runtime.
	GetEventBus() eventbus.EventBus
}

// AgentSpec describes agent identity and structured-output preferences for one run.
// It is attached to [ExecuteRequest.AgentSpec] so custom Runtime implementations can read name, prompts,
// and response format without importing pkg/agent.
type AgentSpec struct {
	Name           string
	Description    string
	SystemPrompt   string
	ResponseFormat *interfaces.ResponseFormat
}

// AgentExecution groups per-run execution inputs for custom Runtime implementations. Sub-structs
// stay stable so callers do not depend on a single flat blob that might be reshaped later.
// Temporal-backed runtimes typically use worker-local configuration for activities; this is a snapshot.
type AgentExecution struct {
	LLM     AgentLLM
	Tools   AgentTools
	Session AgentSession
	Limits  AgentLimits
}

// AgentLLM is the LLM client and sampling overrides for this run.
type AgentLLM struct {
	Client   interfaces.LLMClient
	Sampling *LLMSampling
}

// LLMSampling holds per-run sampling overrides (temperature, max tokens, top-p/k).
// Semantics match agent LLMSampling / internal types.LLMSampling.
type LLMSampling struct {
	Temperature *float64
	MaxTokens   int
	TopP        *float64
	TopK        *int
}

// AgentTools is registered tools, optional registry, and approval policy for this run.
type AgentTools struct {
	Tools          []interfaces.Tool
	Registry       interfaces.ToolRegistry
	ApprovalPolicy interfaces.AgentToolApprovalPolicy
}

// AgentSession is conversation storage and how many messages to include in LLM context.
type AgentSession struct {
	Conversation     interfaces.Conversation
	ConversationSize int
}

// AgentLimits caps iteration and wall-clock behavior for this run.
type AgentLimits struct {
	MaxIterations   int
	Timeout         time.Duration
	ApprovalTimeout time.Duration
}

// ExecuteRequest carries one execution request from Agent to Runtime.
//
// AgentSpec and AgentExecution are populated by pkg/agent from its configuration so implementations
// can read identity (including agent name on AgentSpec.Name), prompts, LLM, tools, and policies
// for this run. Implementations may ignore fields they do not use.
type ExecuteRequest struct {
	UserPrompt       string
	ConversationID   string
	StreamingEnabled bool
	// EventTypes filters streamed events; empty means default (implementation-defined, often all types).
	EventTypes       []types.AgentEventType
	SubAgentRoutes   map[string]types.SubAgentRoute
	MaxSubAgentDepth int

	ApprovalHandler types.ApprovalHandler

	// AgentSpec is identity and output-format metadata for this run (name, description, system prompt, response format).
	AgentSpec *AgentSpec
	// AgentExecution is LLM, tools, conversation, sampling, and policy for this run.
	AgentExecution *AgentExecution
}
