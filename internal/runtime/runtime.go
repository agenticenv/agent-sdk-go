// Package runtime defines internal execution contracts for agent backends (Temporal, in-process, etc.).
// SDK users do not import this package; pkg/agent wires implementations.
// If a file also imports the standard library "runtime" package, alias one import (e.g. agentrt ".../internal/runtime").
package runtime

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	"github.com/agenticenv/agent-sdk-go/internal/types"
)

// Runtime executes agent runs against a backend.
type Runtime interface {
	// Run starts the agent workflow and returns the result. Use WithApprovalHandler when tools require approval (Run only; handler uses req.Respond). RunStream uses AgentEventApproval + OnApproval.
	// Use WithTimeout or a context with deadline to avoid blocking.
	// When using WithConversation, pass the conversation ID (runtime id from user/session); agent and worker use the same ID.
	Run(ctx context.Context, req *RunRequest) (*types.AgentResponse, error)

	// RunStream starts the run and returns a channel of AgentEvent. Events are streamed until
	// AgentEventComplete from this agent (the root of the run). Complete events from delegated
	// sub-agents are still delivered but do not close the stream. After that root complete, the
	// channel stays open until the root workflow run finishes on Temporal (there is often more
	// work after the event, e.g. post-sub-agent activities), then closes.
	// For approvals (tool or delegation), receive AgentEventApproval and call OnApproval as in the streaming examples.
	// When using WithConversation, pass the conversation ID.
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
	// EventTypes filters streamed events; empty means default (implementation-defined).
	EventTypes       []string
	SubAgentRoutes   map[string]types.SubAgentRoute
	MaxSubAgentDepth int

	EnableRemoteWorkers bool
	ApprovalHandler     types.ApprovalHandler
}
