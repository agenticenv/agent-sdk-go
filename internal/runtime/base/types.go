package base

import (
	"crypto/sha256"
	"fmt"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

const scopeKeyParentAgentID = "parent_agent_id"

// LLMResult is the result of a successful LLM call.
// Content holds the assistant text; ToolCalls holds any tool invocations resolved against
// the registered tools list (NeedsApproval pre-computed from the approval policy).
type LLMResult struct {
	Content   string
	ToolCalls []ToolCallRequest
	Usage     *interfaces.LLMUsage
}

// ToolCallRequest describes one tool call returned by the LLM.
// NeedsApproval is pre-computed from the tool approval policy so orchestration loops
// (local agent loop, temporal workflow) do not need to re-evaluate the policy.
type ToolCallRequest struct {
	ToolCallID      string
	ToolName        string
	ToolDisplayName string
	ToolKind        types.ToolKind
	Args            map[string]any
	NeedsApproval   bool
}

// AuthorizeResult is the outcome of a programmatic tool authorization check.
// When Allowed is false, Reason carries the denial message for logging/events.
type AuthorizeResult struct {
	Allowed bool
	Reason  string
}

// RetrieverResult is the outcome of ExecuteRetrievers (prefetch / hybrid pre-loop).
type RetrieverResult struct {
	Context        string
	TotalSearches  int64
	FailedSearches int64
}

// ExecuteRetrieversInput holds per-invocation inputs for [Runtime.ExecuteRetrievers].
// RunID and Iteration populate [hooks.RunMeta] for retrieve middleware hooks.
type ExecuteRetrieversInput struct {
	Logger    logger.Logger
	RunID     string
	Iteration int
	Query     string
}

// MemoryResult is the outcome of ExecuteMemoryRecall.
type MemoryResult struct {
	Context       string
	TotalRecalls  int64
	FailedRecalls int64
}

// ExecuteMemoryRecallInput holds per-invocation inputs for [Runtime.ExecuteMemoryRecall].
// RunID and Iteration populate [hooks.RunMeta] for memory load middleware hooks.
type ExecuteMemoryRecallInput struct {
	Logger    logger.Logger
	RunID     string
	Iteration int
	Scope     interfaces.MemoryScope
	Query     string
}

// StoreMemoryRecordsInput holds per-invocation inputs for [Runtime.StoreMemoryRecords].
// RunID and Iteration populate [hooks.RunMeta] for memory store middleware hooks.
type StoreMemoryRecordsInput struct {
	Logger    logger.Logger
	RunID     string
	Iteration int
	Scope     interfaces.MemoryScope
	Records   []interfaces.MemoryRecord
}

// ExecuteMemoryStoreInput holds per-invocation inputs for [Runtime.ExecuteMemoryStore].
// RunID and Iteration populate [hooks.RunMeta] for memory store middleware hooks.
type ExecuteMemoryStoreInput struct {
	Logger    logger.Logger
	RunID     string
	Iteration int
	Scope     interfaces.MemoryScope
	Messages  []interfaces.Message
}

// ExecuteToolInput holds per-invocation inputs for [Runtime.ExecuteTool].
// RunID and Iteration populate [hooks.RunMeta] for tool middleware hooks.
type ExecuteToolInput struct {
	Logger     logger.Logger
	Tools      []interfaces.Tool
	ToolName   string
	Args       map[string]any
	ToolCallID string
	RunID      string
	Iteration  int
}

// IdempotencyKey returns a stable 32-char hex key for a tool invocation,
// derived from runID, iteration, and toolCallID via SHA-256.
// Fixed-length output is safe for all external API idempotency key fields.
func (i ExecuteToolInput) IdempotencyKey() string {
	raw := fmt.Sprintf("%s:%d:%s", i.RunID, i.Iteration, i.ToolCallID)
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", hash)[:32]
}
