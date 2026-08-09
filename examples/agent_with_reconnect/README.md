# agent_with_reconnect

Focused demo of **`GetAgentStream`**: start a durable stream, simulate a mid-run crash after the first text chunk, then reconnect from a saved `runID` + event offset. Events print their offsets so you can see what to persist.

Works with **Temporal** and **Restate** (single process). Local runtime has no stream offsets. For real process-kill scenarios, see [`durable_agent/temporal`](../durable_agent/temporal/) or [`durable_agent/restate`](../durable_agent/restate/).

## Prerequisites

1. **Configuration** — set up `examples/.env` first: **[Configuration](../README.md#configuration)** in [`examples/README.md`](../README.md).
2. **Working directory** — run commands from `examples/`.
3. **Durable runtime** — Temporal **or** Restate:

```bash
# Temporal
task infra:temporal:up && task infra:temporal:wait
AGENT_RUNTIME=temporal

# Restate
task infra:restate:up && task infra:restate:wait
AGENT_RUNTIME=restate
```

## Run

```bash
AGENT_RUNTIME=temporal go run ./agent_with_reconnect "What time is it?"
# or:
AGENT_RUNTIME=restate go run ./agent_with_reconnect "What time is it?"
```

**What happens**

1. **Phase A** — `Stream` starts; `runID` is taken from `agentStream.ID()` before events are consumed. The example tracks each event’s `Offset()`, then cancels the Events context after the first `TEXT_MESSAGE_CONTENT` chunk (simulated crash) and **leaves Phase A immediately** (does not wait for the channel to drain).
2. **Phase B** — `GetAgentStream(ctx, runID)` + `Events(ctx, WithOffset(lastOffset))` resumes from that offset while the run is still live; remaining events print until `RUN_FINISHED`.

```go
agentStream, err := a.Stream(ctx, prompt, nil)
runID := agentStream.ID()
eventCh, err := agentStream.Events(ctx)
// ... track Offset() on each event; cancel Events after first text chunk ...

agentStream, err = a.GetAgentStream(ctx, runID)
eventCh, err = agentStream.Events(ctx, agent.WithOffset(lastOffset))
```

> **Live run only.** Once the durable run completes, the stream log may no longer be available for replay. This example cancels only the subscriber context so the run stays active for Phase B.

## Caller-side protocol

1. On `Stream` start, save `runID` (`agentStream.ID()`) with your correlation key.
2. Track `Offset()` on each received event.
3. On restart: `GetAgentStream(ctx, savedRunID)`, then `Events(ctx, WithOffset(savedOffset))`.
4. Events at offset ≤ saved offset may be redelivered; discard duplicates if needed.
5. Clear saved state on `RUN_FINISHED` / `RUN_ERROR`.
