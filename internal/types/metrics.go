package types

// Metric name constants for the agent SDK. All runtime implementations emit these names
// so dashboards and alerts work regardless of the underlying execution engine.
//
// Counters: call Metrics.IncrementCounter. Histograms: call Metrics.RecordHistogram.
// Latency histograms are in milliseconds; token histograms are raw counts.
const (
	// Agent API — emitted by Agent.Run (including non-blocking Done/Get usage).
	MetricRunStarted    = "agent.run.started"
	MetricRunCompleted  = "agent.run.completed"
	MetricRunFailed     = "agent.run.failed"
	MetricRunDurationMs = "agent.run.duration_ms"

	// Agent API — emitted by Agent.Stream (dispatch phase only).
	MetricStreamStarted    = "agent.stream.started"
	MetricStreamDispatched = "agent.stream.dispatched"
	MetricStreamFailed     = "agent.stream.failed"
	MetricStreamDurationMs = "agent.stream.duration_ms"

	// Agent API — reconnect and control-plane (GetAgentRun / GetAgentStream / Cancel / Events).
	MetricRunReconnectStarted   = "agent.run.reconnect.started"
	MetricRunReconnectFailed    = "agent.run.reconnect.failed"
	MetricRunReconnectCompleted = "agent.run.reconnect.completed"

	MetricStreamReconnectStarted   = "agent.stream.reconnect.started"
	MetricStreamReconnectFailed    = "agent.stream.reconnect.failed"
	MetricStreamReconnectCompleted = "agent.stream.reconnect.completed"

	MetricStreamEventsStarted    = "agent.stream.events.started"
	MetricStreamEventsFailed     = "agent.stream.events.failed"
	MetricStreamEventsSubscribed = "agent.stream.events.subscribed"
	MetricStreamEventsDurationMs = "agent.stream.events.duration_ms"

	MetricRunCancelStarted   = "agent.run.cancel.started"
	MetricRunCancelFailed    = "agent.run.cancel.failed"
	MetricRunCancelCompleted = "agent.run.cancel.completed"

	MetricStreamCancelStarted   = "agent.stream.cancel.started"
	MetricStreamCancelFailed    = "agent.stream.cancel.failed"
	MetricStreamCancelCompleted = "agent.stream.cancel.completed"

	// Runtime — emitted per LLM Generate / GenerateStream call.
	MetricLLMCallStarted   = "agent.llm.call.started"
	MetricLLMCallCompleted = "agent.llm.call.completed"
	MetricLLMCallFailed    = "agent.llm.call.failed"

	// Runtime — token usage, recorded when provider returns non-nil LLMUsage.
	MetricLLMTokensInput  = "agent.llm.tokens.input"
	MetricLLMTokensOutput = "agent.llm.tokens.output"

	// Runtime — LLM wall-clock latency.
	MetricLLMLatencyMs = "agent.llm.latency_ms"

	// Runtime — emitted per tool.Execute call.
	MetricToolCallStarted   = "agent.tool.call.started"
	MetricToolCallCompleted = "agent.tool.call.completed"
	MetricToolCallFailed    = "agent.tool.call.failed"

	// Runtime — tool wall-clock latency.
	MetricToolLatencyMs = "agent.tool.latency_ms"

	// Runtime — emitted per retriever.Search call (prefetch and hybrid modes).
	MetricRetrieverCallStarted   = "agent.retriever.call.started"
	MetricRetrieverCallCompleted = "agent.retriever.call.completed"
	MetricRetrieverCallFailed    = "agent.retriever.call.failed"

	// Runtime — retriever search wall-clock latency.
	MetricRetrieverLatencyMs = "agent.retriever.latency_ms"

	// Runtime — emitted per memory.Load (recall) call.
	MetricMemoryRecallStarted   = "agent.memory.recall.started"
	MetricMemoryRecallCompleted = "agent.memory.recall.completed"
	MetricMemoryRecallFailed    = "agent.memory.recall.failed"

	// Runtime — memory recall wall-clock latency.
	MetricMemoryRecallLatencyMs = "agent.memory.recall.latency_ms"

	// Runtime — emitted per memory.Store call.
	MetricMemoryStoreStarted   = "agent.memory.store.started"
	MetricMemoryStoreCompleted = "agent.memory.store.completed"
	MetricMemoryStoreFailed    = "agent.memory.store.failed"

	// Runtime — memory store wall-clock latency.
	MetricMemoryStoreLatencyMs = "agent.memory.store.latency_ms"

	// Runtime — semantic dedup lookup before memory.Store (Load for upsert decision).
	MetricMemoryDedupStarted   = "agent.memory.dedup.started"
	MetricMemoryDedupCompleted = "agent.memory.dedup.completed"
	MetricMemoryDedupFailed    = "agent.memory.dedup.failed"
	MetricMemoryDedupLatencyMs = "agent.memory.dedup.latency_ms"

	// Runtime — run-end memory extraction (StoreMode always).
	MetricMemoryExtractStarted   = "agent.memory.extract.started"
	MetricMemoryExtractCompleted = "agent.memory.extract.completed"
	MetricMemoryExtractFailed    = "agent.memory.extract.failed"
	MetricMemoryExtractLatencyMs = "agent.memory.extract.latency_ms"

	// Attribute keys used on both metrics and spans.
	MetricAttrModel      = "model"
	MetricAttrProvider   = "provider"
	MetricAttrTool       = "tool"
	MetricAttrRetriever  = "retriever"
	MetricAttrMemoryKind = "memory.kind"
	// MetricAttrMemoryDedup is "upsert" when an existing record is updated, else "append".
	MetricAttrMemoryDedup = "memory.dedup"
)
