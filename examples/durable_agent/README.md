# durable_agent

Separate **agent** and **worker** processes: `DisableLocalWorker` on the agent, `NewAgentWorker` on the worker, shared options in `[opts/opts.go](opts/opts.go)`. The agent uses `Stream`; events are delivered via **Temporal Workflow Streams** — a durable ordered log hosted inside the workflow. The sample prints streaming event types (`AgentEventTypeTextMessageContent`, tool events, `AgentEventTypeCustom` for approvals, `AgentEventTypeRunFinished`, etc. — see `[agent/main.go](agent/main.go)`).

The agent also demonstrates reconnect end to end — it persists `runID` and the last event offset to `/tmp/durable_agent_runstate.json` on every event. Kill the process mid-stream (`pkill -SIGKILL`) and restart **while the worker is still processing**: the agent detects the saved state, asks whether to reconnect, and resumes the stream from the exact offset where it stopped via `GetAgentStream` + `WithOffset`. See **Scenario 5c** below for the full walkthrough.

> **Reconnect requires a live workflow.** Once the workflow completes, its stream log is no longer available for replay. If you restart after the worker has already finished the run, `GetAgentStream` returns an error and prints a prompt to re-submit — the worker finished the work but the events are gone. Timing matters: reconnect while the workflow is still running.

```go
// runID is saved before consuming the channel — survives even a kill -9.
agentStream, err := a.Stream(ctx, prompt, nil)
runID := agentStream.ID()
eventCh, err := agentStream.Events(ctx)
saveRunState(runID, 0, prompt)          // persisted to /tmp/durable_agent_runstate.json
// ... each event: saveRunState(runID, evOffset, prompt)
// ... on RUN_FINISHED/RUN_ERROR: clearRunState()

// On restart:
agentStream, _ = a.GetAgentStream(ctx, savedRunID)
eventCh, _ = agentStream.Events(ctx, agent.WithOffset(savedOffset))
```

See `[agent_with_reconnect/](../agent_with_reconnect/)` for a focused end-to-end reconnect demo outside the durable_agent REPL.

## Prerequisites

This example **always uses Temporal** — shared opts wire `WithTemporalConfig` directly (there is no local-runtime mode).

1. **Configuration** — set up `examples/.env`, env load order, and the run flow first: **[Configuration](../README.md#configuration)** in `[examples/README.md](../README.md)`.
2. **Working directory** — run all commands from the `examples/` directory.
3. **Runtime and Temporal connection** — add to `examples/.env` (or export). Defaults match local Docker compose; change for Temporal Cloud or a custom cluster:

```bash
AGENT_RUNTIME=temporal
TEMPORAL_HOST=127.0.0.1
TEMPORAL_PORT=7233
TEMPORAL_NAMESPACE=default
TEMPORAL_TASKQUEUE=agent-sdk-go-durable-agent
```

Both `agent` and `worker` read the same values via `[config.LoadFromEnv()](../config.go)`. Override any `TEMPORAL_*` variable for your environment.

4. **Temporal server** — Docker must be available. Start with:

```bash
task infra:temporal:up && task infra:temporal:wait
```

That starts the compose dev server on `localhost:7233` (Web UI on port 8233). For Temporal CLI, Cloud, or other hosts, see `[../../temporal-setup.md](../../temporal-setup.md)`.

> **Temporal Web UI** — while the worker and agent are running, open [http://localhost:8233](http://localhost:8233) to inspect workflows and activities (namespace `default`, task queue `agent-sdk-go-durable-agent` by default). Useful when stepping through the durability scenarios below.

Stop Temporal server when finished:

```bash
task infra:temporal:down
```



## Quick start

```bash
# Terminal 1 — worker
go run ./durable_agent/worker

# Terminal 2 — agent (one-shot)
go run ./durable_agent/agent "Hello from remote agent!"
```

For interactive REPL and durability scenarios, see below.

## Scenarios to try (durability)

Use two or three terminals: **terminal 1 = worker** (and **terminal 3 = second worker** or kill commands where noted), **terminal 2 = agent**. Temporal keeps workflow state; these exercises show how the split behaves when processes or connectivity change.

> Run all commands from the `examples/` directory.
> ⚠️ **Start the worker before typing a prompt in the agent REPL.** The worker poller check runs at prompt submission (~**15 seconds**), not at agent start. Run timeout for this example is **3 minutes** (`WithTimeout` in `[opts/opts.go](opts/opts.go)`).
>
> **Clean up between scenarios.** Stop agent and worker processes before each scenario so stale pollers or orphan `go-build` binaries do not skew results (especially scenarios 2 and 7):
>
> ```bash
> pkill -SIGKILL -f 'go run ./durable_agent/worker|go-build/.*/worker' 2>/dev/null; pkill -SIGKILL -f 'go run ./durable_agent/agent|go-build/.*/agent' 2>/dev/null; true
> ```
>
> **LLM reply text varies by model and run.** Labels below: **Expected startup output** (fixed banners), **Sample response shape** (structure only — not exact wording), **Expected behavior** (errors/timeouts).

---



### 1. Baseline — worker first

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Expected startup output:

```text
Agent worker starting on task queue "agent-sdk-go-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2 — start the agent:**

```bash
go run ./durable_agent/agent
```

Expected startup output:

```text
=== durable_agent interactive stream ===
Events are delivered via Temporal Workflow Streams (durable, replayable).
Simulate scenarios: kill the worker or this process mid-run, then restart.
Type 'exit' or 'quit' or 'bye' to stop.

you>
```

**Terminal 2 — type a prompt:**

```text
Hello from remote agent!
```

Sample response shape (LLM text varies):

```text
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Confirms the remote worker path works end to end.

---



### 2. Agent without worker (intentional timeout)

**Stop any running worker first** (see cleanup command above) so path **A** is the typical outcome.

**Terminal 2 — start the agent with no worker running:**

```bash
go run ./durable_agent/agent
```

Wait for the REPL prompt, then type:

```text
Hello from remote agent!
```

> **Note:** This scenario intentionally exercises the no-worker path. With no pollers on the task queue, the SDK checks Temporal before starting the stream (~**15 seconds**). If a worker **was** running recently, Temporal may still list stale pollers briefly — the stream can start, the workflow queues, and you wait for the run timeout instead (**3 minutes** in this example via `WithTimeout` in `[opts/opts.go](opts/opts.go)`). After either error, start the worker and resend — that is the expected flow.

**Expected behavior — depends on timing:**

**A. No pollers visible** (typical when no worker was ever started, or Temporal shows an empty queue):

```text
[error] failed to start stream: no workers available on task queue agent-sdk-go-durable-agent

you>
```

No `--- stream start ---` — the stream never opened.

**B. Stale pollers or workflow already queued** (e.g. worker ran then stopped; Temporal still reports pollers for a short window):

```text
--- stream start ---

[error] context deadline exceeded
--- stream end ---

you>
```

Check the Temporal Web UI — a workflow may appear even when no worker is executing tasks.

**Learn:** The first error is expected — interactive mode fails fast when no worker is polling, instead of hanging forever.

**Terminal 1 — now start the worker:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — resend the prompt:**

```text
Hello from remote agent!
```

Sample response shape (LLM text varies):

```text
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

**Learn:** After the worker is running, resend the same prompt — run succeeds.

---



### 3a. Kill worker between LLM rounds — graceful stop (planned shutdown)

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Wait for:

```text
Agent worker starting on task queue "agent-sdk-go-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2 — start the agent and send a prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Hello from remote agent!
```

Wait for `--- stream end ---`.

**Terminal 1 — stop the worker gracefully:**

```text
^C
Shutdown signal received; stopping worker (may wait for in-flight activities)...
Agent worker stopped.
```

**Terminal 1 — restart the worker:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — send another prompt:**

```text
you> Hello again!
```

Sample response shape (LLM text varies):

```text
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Simulates a planned worker shutdown — deploy, upgrade, or config change. Completed activity results are already recorded in Temporal workflow history — the restarted worker does not re-execute them.

---



### 3b. Kill worker between LLM rounds — crash (unplanned shutdown)

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Wait for:

```text
Agent worker starting on task queue "agent-sdk-go-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2 — start the agent and send a prompt:**

```bash
go run ./durable_agent/agent
```

```text
you> Hello from remote agent!
```

Wait for `--- stream end ---`.

**Terminal 3 — kill the worker** (parent + child):

```bash
pkill -SIGKILL -f 'go run ./durable_agent/worker|go-build/.*/worker'
```

Terminal 1 exits immediately with no cleanup — simulating a real worker crash.

**Terminal 1 — restart the worker:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — send another prompt:**

```text
you> Hello again!
```

Sample response shape (LLM text varies):

```text
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Simulates an unexpected worker crash. Temporal detects the worker is gone and reschedules pending tasks on the restarted worker. No state is lost — completed activity results are already in workflow history and are not re-executed.

---



### 4a. Kill worker during an LLM call — worker stays down (timeout)

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Wait for:

```
Agent worker starting on task queue "agent-sdk-go-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2 — start the agent and send a longer prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

Watch tokens streaming in terminal 2. While streaming is active, **kill the worker from terminal 3** (parent + child; use `pkill -SIGKILL` — Ctrl+C or plain `pkill` trigger graceful shutdown) **and do not restart it:**

```bash
pkill -SIGKILL -f 'go run ./durable_agent/worker|go-build/.*/worker'
```

Terminal 1 exits immediately — no worker is polling the queue.

What to look for in terminal 2 (stream pauses, no immediate error):

```text
--- stream start ---
<tokens streaming… e.g. travel plan text>
```

The stream goes silent — Temporal is waiting for a worker to poll and resume the in-flight activity. No worker is available so no events are sent to the agent stream. After the run timeout (**3 minutes** in this example) the error surfaces:

```text
[error] request timed out (approval expired or deadline exceeded)
--- stream end ---

you>
```

This confirms the agent fails clearly on timeout rather than hanging indefinitely. Start the worker again to resume normal operation:

```bash
go run ./durable_agent/worker
```

---



### 4b. Kill worker during an LLM call — worker restarts before timeout (stream resumes)

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Wait for:

```
Agent worker starting on task queue "agent-sdk-go-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2 — start the agent and send a longer prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

Watch tokens streaming in terminal 2. While streaming is active, **kill the worker from terminal 3** (parent + child; use `pkill -SIGKILL` — Ctrl+C triggers graceful shutdown that may wait for the in-flight LLM activity):

```bash
pkill -SIGKILL -f 'go run ./durable_agent/worker|go-build/.*/worker'
```

Terminal 1 exits immediately.

What to look for in terminal 2 (stream pauses, no immediate error):

```
--- stream start ---
<tokens streaming… e.g. travel plan text>
```

**Terminal 1 — restart the worker before the timeout (within 3 minutes):**

```bash
go run ./durable_agent/worker
```

Temporal reschedules the in-flight LLM activity on the restarted worker. The LLM call reruns (activities are retried, not replayed) and the stream resumes automatically in terminal 2:

> **Note:** When the worker restarts and the LLM activity reruns, you may see overlapping or repeated tokens in the stream — this is expected. The activity retries from the beginning so streaming output may duplicate content already seen before the worker stopped. The final stored response in conversation history is always the single complete result, not the duplicated stream chunks.

```
--- stream start ---
<stream resumes; tokens may repeat from activity retry>
…
--- stream end ---

you>
```

No prompt resend needed — the workflow resumed from where Temporal left off and the stream continued on the same agent process. This is the core durability guarantee of the SDK.

---



### 5a. Graceful agent restart (planned shutdown)

**Terminal 1 — start the worker and leave it running:**

```bash
go run ./durable_agent/worker
```

Wait for:

```
Agent worker starting on task queue "agent-sdk-go-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2 — start the agent and send a prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Hello from remote agent!
```

Wait for `--- stream end ---`, then type `bye` to stop the agent gracefully:

```
you> bye
Goodbye!
```

**Terminal 2 — start the agent again:**

```bash
go run ./durable_agent/agent
```

```
you> Hello again!
```

Sample response shape (LLM text varies):

```
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Simulates a planned restart — deploy, upgrade, or config change. The new agent process drives work through the same Temporal namespace and task queue without any reconfiguration.

---



### 5b. Agent crash (unplanned shutdown)

**Terminal 1 — start the worker and leave it running:**

```bash
go run ./durable_agent/worker
```

Wait for:

```
Agent worker starting on task queue "agent-sdk-go-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2 — start the agent and send a prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Hello from remote agent!
```

Wait for `--- stream end ---`.

**Terminal 3 — kill the agent** (parent + child):

```bash
pkill -SIGKILL -f 'go run ./durable_agent/agent|go-build/.*/agent'
```

Terminal 2 exits immediately with no cleanup — simulating a real crash (OOM, hardware failure, or unhandled panic).

**Terminal 2 — start the agent again:**

```bash
go run ./durable_agent/agent
```

```text
you> Hello again!
```

Sample response shape (LLM text varies):

```
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Simulates an unexpected crash. The worker never stopped — it continues polling the same task queue. The new agent process connects to the same Temporal namespace and resumes work immediately. No state is lost.

---



### 5c. Agent crash mid-LLM call — reconnect while the workflow is still running

This scenario shows `GetAgentStream` in action: the agent crashes while streaming, but the worker is still processing the LLM call. Restarting the agent quickly and reconnecting resumes the stream from the saved offset — buffered events replay, then live events continue as the worker finishes.

> **Timing is critical.** Reconnect only works while the workflow is still running. Use a **long prompt** (e.g. a 7-day travel plan) so the LLM call takes several seconds — giving you enough time to kill the agent and restart before the worker finishes. If you restart after the workflow has already completed, the agent prints a clear message and asks you to re-submit.

**Terminal 1 — start the worker and leave it running throughout:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — start the agent and send a long prompt:**

```bash
go run ./durable_agent/agent
```

```text
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

Watch the `[run_id]` line printed before any tokens arrive — the state file is already written:

```text
[run_id] <uuid>
--- stream start ---
Day 1: Arrival in Tokyo...
```

While tokens are streaming (within the first few seconds), **kill the agent from terminal 3**:

```bash
pkill -SIGKILL -f 'go run ./durable_agent/agent|go-build/.*/agent'
```

Terminal 2 exits immediately. **Terminal 1 — the worker keeps running**, still mid-LLM call.

Verify the state file was saved before the kill:

```bash
cat /tmp/durable_agent_runstate.json
```

```json
{"run_id":"<uuid>","offset":10,"prompt":"Write a detailed day-by-day travel plan for a 7-day trip to Japan."}
```

**Terminal 2 — restart the agent quickly** (while the worker is still processing):

```bash
go run ./durable_agent/agent
```

On startup the saved state is detected:

```text
[reconnect] found saved run state:
  run_id : <uuid>
  offset : 10
  prompt : "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
Reconnect from last offset? [y/n]>
```

Type `y`. The agent reconnects from offset 10, replays any buffered events, then receives live events as the worker finishes:

```text
[reconnect] reconnecting run_id=<uuid> from offset=10
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."

--- stream resumed ---
<continuation from where the agent died — buffered + live events>
...Day 7: Farewell day in Kyoto...
--- stream end ---
```

The state file is cleared on `RUN_FINISHED`. The REPL then continues normally.

**If the workflow already completed before you reconnected**, the agent prints:

```text
[reconnect] the run completed successfully while you were disconnected.
[reconnect] the response was generated, but streaming events are no longer available.
[reconnect] if conversation history is configured, the response is already saved —
[reconnect] start a new turn to continue. otherwise, start a new run.
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
```

The state file is cleared. The work **was done** — Temporal completed the run and the LLM generated the full response. The loss is only the streaming view of it:

- **With `WithConversation` configured** — the response is already in conversation history. Start a new turn and continue naturally; the agent remembers the previous answer.
- **Without conversation (this example)** — the response is not visible. Start a new run to get a fresh response.

> **Note:** The `durable_agent` example does not wire up multi-turn conversation history. For production apps where users expect full history across restarts, use **[Agent Chat](https://github.com/agenticenv/agent-chat)** which persists conversation turns to Postgres.

---



### 6. Two workers, one queue

**Terminal 1 — start the first worker:**

```bash
go run ./durable_agent/worker
```

**Terminal 3 — start a second worker with the same config:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — start the agent and send prompts:**

```bash
go run ./durable_agent/agent
```

```text
you> Hello from remote agent!
```

**Ctrl+C one worker** after the first reply completes (or during a long prompt if both workers are up — the remaining worker keeps the run alive), then send another prompt:

```text
you> Still working?
```

Sample response shape (LLM text varies):

```text
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Both workers poll the same task queue — Temporal distributes the load automatically. Stopping one worker mid-session does not drop a run.

---



### 7. Task queue mismatch

**Stop any worker on the default queue first** (cleanup command above). Point the **worker** at a different queue via env (no code edits). Agent keeps the default `agent-sdk-go-durable-agent` from `.env`:

**Terminal 1:**

```bash
TEMPORAL_TASKQUEUE=wrong-queue-durable-agent go run ./durable_agent/worker
```

Expected startup output:

```text
Agent worker starting on task queue "wrong-queue-durable-agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop (twice to force quit if shutdown hangs).
```

**Terminal 2:**

```bash
go run ./durable_agent/agent
```

```text
you> Hello from remote agent!
```

> **Note:** Same timing as scenario 2 — the agent checks pollers on **`agent-sdk-go-durable-agent`**, not the worker’s queue. With no pollers there (~**15 seconds**), path **A** is typical. Stale pollers on the agent queue from an earlier run can produce path **B** (**3 minutes** run timeout).

**Expected behavior — depends on timing:**

**A. No pollers on the agent queue** (typical — worker polls `wrong-queue-durable-agent` only):

```text
[error] failed to start stream: no workers available on task queue agent-sdk-go-durable-agent

you>
```

No `--- stream start ---` — the stream never opened.

**B. Stale pollers on the agent queue** (e.g. a correct worker ran earlier; Temporal still reports pollers briefly):

```text
--- stream start ---

[error] request timed out (approval expired or deadline exceeded)
--- stream end ---

you>
```

Check the Temporal Web UI — a workflow may appear on `agent-sdk-go-durable-agent` even though the only running worker polls a different queue.

Misconfiguration surfaces clearly rather than silently corrupting state. Stop the worker and restart both processes with matching `TEMPORAL_TASKQUEUE` (or remove the override) to recover.

> **Tip:** For `AgentModeAutonomous`, the worker check is skipped entirely — a task queue mismatch will not error immediately but will cause the workflow to queue in Temporal until the agent timeout hits. Always verify task queue names match across agent and worker config before deploying.


