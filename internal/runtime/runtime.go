// Package runtime defines internal execution contracts for agent backends (Temporal, in-process, etc.).
// SDK users do not import this package; pkg/agent wires implementations.
// If a file also imports the standard library "runtime" package, alias one import (e.g. agentrt ".../internal/runtime").
package runtime

import (
	"context"
	"errors"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

//go:generate mockgen -destination=./mocks/mock_runtime.go -package=mocks github.com/agenticenv/agent-sdk-go/internal/runtime Runtime

// ErrApprovalNotSupported is returned by Runtime.Approve when the runtime does not use token-based approval.
var ErrApprovalNotSupported = errors.New("runtime: approval not supported")

// Runtime executes agent runs against a backend.
type Runtime interface {
	// Run starts one execution and returns the result. The agent package supplies approval via RunRequest when needed.
	// Use WithTimeout or a context with deadline to avoid blocking.
	// When using conversation, pass the conversation ID on the request; agent and worker must use the same ID.
	Run(ctx context.Context, req *RunRequest) (*types.AgentResponse, error)

	// RunStream starts the run and returns a channel of AgentEvent. Events are streamed until
	// AgentEventComplete from this agent (the root of the run). Complete events from delegated
	// sub-agents are still delivered but do not close the stream. After that root complete, the
	// channel may remain open until the implementation finishes the run (e.g. backend cleanup), then closes.
	// For approvals (tool or delegation), receive AgentEventApproval and call the approval path
	// provided by the agent package (e.g. OnApproval in streaming examples).
	// When using conversation, pass the conversation ID on the request.
	RunStream(ctx context.Context, req *RunRequest) (chan *types.AgentEvent, error)

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

// RunRequest carries one execution request from Agent to Runtime.
// SpecFingerprint is optional: workers may reject execution if it does not match local spec.
type RunRequest struct {
	AgentName        string
	UserPrompt       string
	ConversationID   string
	StreamingEnabled bool
	SpecFingerprint  string
	// EventTypes filters streamed events; empty means default (implementation-defined, often all types).
	EventTypes       []types.AgentEventType
	SubAgentRoutes   map[string]types.SubAgentRoute
	MaxSubAgentDepth int

	EnableRemoteWorkers bool
	ApprovalHandler     types.ApprovalHandler
}
