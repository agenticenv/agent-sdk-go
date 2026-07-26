# agent_with_reconnect

Focused demo of **`GetAgentStream`**: start a Temporal stream, simulate a mid-run crash after the first text chunk, then reconnect from a saved `runID` + event offset. Events print their offsets so you can see what to persist.

Shared agent/worker options live in [`opts/opts.go`](opts/opts.go). For crash/kill scenarios across separate processes, see [`durable_agent`](../durable_agent/).

## Prerequisites

This example **requires Temporal** (`WithTemporalConfig` in shared opts). Local runtime has no stream offsets, so reconnect is unsupported.

1. **Configuration** — set up `examples/.env` first: **[Configuration](../README.md#configuration)** in [`examples/README.md`](../README.md).
2. **Working directory** — run commands from `examples/`.
3. **Temporal** — start the server:

```bash
task infra:temporal:up && task infra:temporal:wait
```

Set (or accept defaults from `.env.defaults`):

```bash
AGENT_RUNTIME=temporal
TEMPORAL_HOST=127.0.0.1
TEMPORAL_PORT=7233
TEMPORAL_NAMESPACE=default
TEMPORAL_TASKQUEUE=agent-sdk-go   # or your override
```

## Quick start (single process)

Embedded worker — no second terminal:

```bash
AGENT_RUNTIME=temporal go run ./agent_with_reconnect/agent "What time is it?"
```

**What happens**

1. **Phase A** — `Stream` starts; `runID` is taken from `agentStream.ID()` before events are consumed. The example tracks each event’s `Offset()`, then cancels the stream context after the first `TEXT_MESSAGE_CONTENT` chunk (simulated crash).
2. **Phase B** — `GetAgentStream(ctx, runID)` + `Events(ctx, WithOffset(lastOffset))` resumes from that offset; remaining events (replay + live) print until `RUN_FINISHED`.

```go
agentStream, err := a.Stream(ctx, prompt, nil)
runID := agentStream.ID()
eventCh, err := agentStream.Events(ctx)
// ... track Offset() on each event; cancel after first text chunk ...

agentStream, err = a.GetAgentStream(ctx, runID)
eventCh, err = agentStream.Events(ctx, agent.WithOffset(lastOffset))
```

> **Live workflow only.** Once the workflow completes, the stream log is no longer available for replay. This example cancels only the subscriber context so the embedded worker keeps running and the workflow stays active for Phase B.

## Optional: separate worker

```bash
# Terminal 1
AGENT_RUNTIME=temporal go run ./agent_with_reconnect/worker

# Terminal 2
AGENT_RUNTIME=temporal go run ./agent_with_reconnect/agent "What time is it?"
```

The agent binary uses an **embedded** worker by default. For a true agent/worker split (`DisableLocalWorker`), use [`agent_with_worker`](../agent_with_worker/) or [`durable_agent`](../durable_agent/).

## Caller-side protocol

1. On `Stream` start, save `runID` (`agentStream.ID()`) with your correlation key.
2. Track `Offset()` on each received event.
3. On restart: `GetAgentStream(ctx, savedRunID)`, then `Events(ctx, WithOffset(savedOffset))`.
4. Discard events at offset ≤ saved offset if you need strict deduplication.
5. Clear saved state on `RUN_FINISHED` or `RUN_ERROR`.
