// Package hooks defines agent middleware hook types used by the SDK runtime.
// Register hooks via [github.com/agenticenv/agent-sdk-go/pkg/agent.WithHooks]; types are re-exported
// from the agent package for application use.
package hooks

// AgentHooks defines middleware hooks that fire at key points in the agent execution lifecycle.
// Multiple hooks can be registered per execution point and are chained in declaration order.
// Each hook receives the (possibly modified) output of the previous hook in the chain.
// Any hook returning an error aborts the remaining chain and halts execution.
//
// Common use cases by category:
//
//   - Guardrails and safety: block bad inputs, prompt-injection checks ([BeforeLLMHook]);
//     output filtering ([AfterLLMHook]).
//   - PII and privacy: scrub prompts and responses ([BeforeLLMHook], [AfterLLMHook]); tool args
//     ([BeforeToolHook], [AfterToolHook]); retrieval query and documents ([BeforeRetrieveHook],
//     [AfterRetrieveHook]); memory load ([BeforeMemoryLoadHook], [AfterMemoryLoadHook]) and
//     store ([BeforeMemoryStoreHook], [AfterMemoryStoreHook]).
//   - Cost and caching: token tracking and budgets ([AfterLLMHook], [BeforeLLMHook]); LLM response
//     cache read/write ([BeforeLLMHook], [AfterLLMHook]).
//   - Rate limiting and validation: per-tool input scrubbing and rate limits ([BeforeToolHook]);
//     retrieval query validation ([BeforeRetrieveHook]).
//   - Logging and audit: LLM, tool, retrieval, and memory operations (before/after hooks on
//     each area).
//   - Resilience: model fallback ([AfterLLMHook]); retrieval retry or re-rank ([AfterRetrieveHook]).
//   - Memory control: scope filtering and tenant isolation ([BeforeMemoryLoadHook],
//     [BeforeMemoryStoreHook]); inspect injected context ([AfterMemoryLoadHook]).
type AgentHooks struct {
	// LLM hooks fire on every model call.
	// BeforeLLM — guardrails, PII redaction, prompt injection detection, caching, input validation
	// AfterLLM  — cost tracking, PII scrubbing, fallback model swap, cache store, token budget enforcement
	BeforeLLM []BeforeLLMHook
	AfterLLM  []AfterLLMHook

	// Tool hooks fire for native and MCP tools only, after authorization and approval.
	// BeforeTool — input scrubbing, rate limiting, arg mutation
	// AfterTool  — result scrubbing, logging, result transformation
	BeforeTool []BeforeToolHook
	AfterTool  []AfterToolHook

	// Retrieve hooks fire for both prefetch and agentic RAG paths.
	// BeforeRetrieve — query rewriting, PII scrubbing, query validation
	// AfterRetrieve  — result filtering, re-ranking, result logging
	BeforeRetrieve []BeforeRetrieveHook
	AfterRetrieve  []AfterRetrieveHook

	// Memory hooks fire on memory read and write operations.
	// BeforeMemoryLoad  — query/load-option mutation; scope is read-only context on input
	// AfterMemoryLoad  — filter or rewrite prompt context injected into the LLM
	// BeforeMemoryStore — scrub PII before persisting, control what gets stored
	// AfterMemoryStore  — audit persisted memories, logging
	BeforeMemoryLoad  []BeforeMemoryLoadHook
	AfterMemoryLoad   []AfterMemoryLoadHook
	BeforeMemoryStore []BeforeMemoryStoreHook
	AfterMemoryStore  []AfterMemoryStoreHook

	// OnBudgetExceeded fires each time a per-run budget limit is breached, before the
	// configured action (stop or approval pause) is taken. Fire-and-forget — use it for
	// metrics, alerts, and audit logging. Errors are not propagated.
	OnBudgetExceeded []OnBudgetExceededHook
}

// HookGroup is a named set of middleware hooks registered via
// [github.com/agenticenv/agent-sdk-go/pkg/agent.WithHooks].
type HookGroup struct {
	// Name is the unique hook group identifier used for Temporal fingerprinting and [RunMeta].
	Name string

	// Hooks are the middleware functions in this group.
	Hooks AgentHooks
}

// RunMeta carries read-only execution context shared across hooks in a run.
// Hooks must not modify RunMeta; the runtime populates it when firing hooks.
type RunMeta struct {
	// RunID is the stable identifier for the current agent run.
	RunID string

	// Iteration is the zero-based LLM loop round (0 for the first model call).
	Iteration int

	// HooksGroup is the [github.com/agenticenv/agent-sdk-go/pkg/agent.WithHooks] group name for the
	// hook currently executing. The runtime sets this from the validated group name when firing hooks.
	HooksGroup string
}

// Merge combines two AgentHooks by appending each hook slice in order.
// Hooks from other are appended after hooks already present in h.
// Nil or empty slices in either value are ignored by append.
func (h AgentHooks) Merge(other AgentHooks) AgentHooks {
	return AgentHooks{
		BeforeLLM: append(h.BeforeLLM, other.BeforeLLM...),
		AfterLLM:  append(h.AfterLLM, other.AfterLLM...),

		BeforeTool: append(h.BeforeTool, other.BeforeTool...),
		AfterTool:  append(h.AfterTool, other.AfterTool...),

		BeforeRetrieve: append(h.BeforeRetrieve, other.BeforeRetrieve...),
		AfterRetrieve:  append(h.AfterRetrieve, other.AfterRetrieve...),

		BeforeMemoryLoad:  append(h.BeforeMemoryLoad, other.BeforeMemoryLoad...),
		AfterMemoryLoad:   append(h.AfterMemoryLoad, other.AfterMemoryLoad...),
		BeforeMemoryStore: append(h.BeforeMemoryStore, other.BeforeMemoryStore...),
		AfterMemoryStore:  append(h.AfterMemoryStore, other.AfterMemoryStore...),

		OnBudgetExceeded: append(h.OnBudgetExceeded, other.OnBudgetExceeded...),
	}
}
