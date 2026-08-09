# durable_agent / restate

Single-process durability lab on **Restate**. `NewAgent` embeds the Restate SDK endpoint. Events are durable via Restate; kill the agent mid-stream, keep Restate running, restart, and reconnect with `GetAgentStream` + `WithOffset`.

State file: `/tmp/durable_agent_restate_runstate.json`.

For the Temporal split worker/agent lab, see `[../temporal/](../temporal/)`.

## Prerequisites

1. **Configuration** — `examples/README.md` [Configuration](../../README.md#configuration).
2. **Working directory** — run commands from `examples/`.
3. **Env** — add to `examples/.env` (or export):

```bash
AGENT_RUNTIME=restate
RESTATE_INGRESS_URL=http://localhost:8080
RESTATE_ADMIN_URL=http://localhost:9070
RESTATE_ENDPOINT_LISTEN_ADDRESS=:9080
# When Restate runs in Docker and this process on the host:
# RESTATE_DEPLOYMENT_URL=http://host.docker.internal:9080
```

1. **Restate server**:

```bash
task infra:restate:up && task infra:restate:wait
```

Or see `[../../../restate-setup.md](../../../restate-setup.md)`. Admin UI: [http://localhost:9070](http://localhost:9070).

Stop when finished: `task infra:restate:down`.

## Quick start

```bash
# From examples/
go run ./durable_agent/restate "Hello from Restate durable agent!"
```

Interactive REPL (no args):

```bash
go run ./durable_agent/restate
```

Type prompts at `you>`. Type `exit` / `quit` / `bye` to stop.

## Scenarios to try (durability)

Single process: this binary embeds the Restate SDK endpoint. Use **two terminals** when you kill/restart the agent (`terminal 1` = agent REPL, `terminal 2` = kill / `cat` / copy commands). Keep Restate up for the whole lab (`task infra:restate:up`).

> Run all commands from the `examples/` directory.
>
> **Reconnect requires a live Restate invocation.** Once the run finishes, streaming events are no longer available for replay. If you restart after Restate has already completed the run, `GetAgentStream` returns `ErrRunAlreadyCompleted` and the agent asks you to start a new turn. Timing matters for scenarios 2–3: reconnect while the invocation is still running. Scenario 4 deliberately waits until it has finished.
>
> **Clean up between scenarios** (optional — clears a leftover state file and any stray agent process):
>
> ```bash
> pkill -SIGKILL -f 'go run ./durable_agent/restate|go-build/.*/restate' 2>/dev/null; rm -f /tmp/durable_agent_restate_runstate.json; true
> ```
>
> **LLM reply text varies by model and run.** Labels below: **Expected startup output** (fixed banners), **Sample response shape** (structure only), **Expected behavior**.

---

### 1 — Happy path

**Terminal 1 — start the agent:**

```bash
go run ./durable_agent/restate
```

Expected startup output:

```text
=== durable_agent/restate interactive stream ===
Events are durable via Restate (embedded SDK endpoint).
Kill this process mid-stream (Ctrl+C / pkill), keep Restate up, then restart to reconnect.
Type 'exit' or 'quit' or 'bye' to stop.

you>
```

**Terminal 1 — type a short prompt:**

```text
Hello from Restate durable agent!
```

Sample response shape (LLM text varies):

```text
[run_id] <uuid>
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Optional: open the Restate Admin UI at [http://localhost:9070](http://localhost:9070) and confirm the invocation completed.

Type `bye` when finished, or leave the REPL open for the next scenario.

---

### 2 — Kill agent mid-stream, reconnect (crash)

This scenario shows `GetAgentStream` + `WithOffset`: the agent process is killed while streaming, Restate keeps the durable invocation, and a restart resumes from the saved offset.

> **Timing is critical.** Use a **long prompt** so the LLM call takes several seconds — enough time to kill, check the state file, and restart before Restate finishes. If you wait too long, you get the “already completed” path (that is scenario 4).

**Terminal 1 — start the agent and send a long prompt:**

```bash
go run ./durable_agent/restate
```

```text
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

Watch the `[run_id]` line — the state file is written before tokens arrive:

```text
[run_id] <uuid>
--- stream start ---
Day 1: Arrival in Tokyo...
```

While tokens are streaming (within the first few seconds), **kill the agent from terminal 2**:

```bash
pkill -SIGKILL -f 'go run ./durable_agent/restate|go-build/.*/restate'
```

Terminal 1 exits immediately (no cleanup) — simulating a crash. **Restate keeps running.**

**Terminal 2 — confirm the state file was saved:**

```bash
cat /tmp/durable_agent_restate_runstate.json
```

```json
{"run_id":"<uuid>","offset":10,"prompt":"Write a detailed day-by-day travel plan for a 7-day trip to Japan."}
```

**Terminal 1 — restart quickly** (while the Restate invocation is still live):

```bash
go run ./durable_agent/restate
```

On startup the saved state is detected:

```text
[reconnect] found saved run state:
  run_id : <uuid>
  offset : 10
  prompt : "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
Reconnect from last offset? [y/n]>
```

Type `y`. The agent reconnects from the saved offset, skips already-seen events, then continues live events as Restate finishes the run:

```text
[reconnect] reconnecting run_id=<uuid> from offset=10
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."

--- stream resumed ---
<continuation from where the agent died — new events only>
...Day 7: Farewell day in Kyoto...
--- stream end ---
```

The state file is cleared on `RUN_FINISHED`. The REPL then continues normally.

---

### 3 — Graceful Ctrl+C mid-stream, then reconnect

Same goal as scenario 2 (resume a live run), but stop with a **planned** Ctrl+C instead of `pkill -SIGKILL`. Restate still holds the durable invocation; restart and reconnect while it is live.

**Terminal 1 — start the agent and send a long prompt:**

```bash
go run ./durable_agent/restate
```

```text
you> Write a detailed essay about durable execution and why crash recovery matters.
```

Wait until you see streaming tokens:

```text
[run_id] <uuid>
--- stream start ---
Durable execution means...
```

**Terminal 1 — press Ctrl+C once** while tokens are still streaming.

Expected shutdown output:

```text
Shutdown signal received; closing agent...
durable_agent/restate stopped.
```

(If shutdown hangs, press Ctrl+C a second time to force exit.)

**Terminal 2 — confirm state was saved before exit:**

```bash
cat /tmp/durable_agent_restate_runstate.json
```

```json
{"run_id":"<uuid>","offset":8,"prompt":"Write a detailed essay about durable execution and why crash recovery matters."}
```

**Terminal 1 — restart quickly** (while Restate still has the live invocation):

```bash
go run ./durable_agent/restate
```

```text
[reconnect] found saved run state:
  run_id : <uuid>
  offset : 8
  prompt : "Write a detailed essay about durable execution and why crash recovery matters."
Reconnect from last offset? [y/n]>
```

Type `y`.

Expected behavior (run still live):

```text
[reconnect] reconnecting run_id=<uuid> from offset=8
[reconnect] original prompt: "Write a detailed essay about durable execution and why crash recovery matters."

--- stream resumed ---
<continuation>
--- stream end ---
```

**Learn:** Ctrl+C is a planned process shutdown; Restate durability is independent of this process. Reconnect works the same as after a hard kill **as long as** the invocation has not already completed.

---

### 4 — Restart after the run already finished

This is the opposite of scenarios 2–3: prove that reconnect **fails cleanly** when Restate has already completed the run. Streaming events are not available after completion (`ErrRunAlreadyCompleted`).

> On a normal `RUN_FINISHED`, this example **deletes** `/tmp/durable_agent_restate_runstate.json`. So finishing a short prompt and restarting usually shows **no** reconnect prompt. To exercise the completed-run path, you must keep a copy of the state file, wait for Restate to finish, then put the stale file back.

**Step A — create a mid-run state file (same start as scenario 2)**

**Terminal 1 — start and send a long prompt:**

```bash
go run ./durable_agent/restate
```

```text
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

As soon as you see `[run_id]` / streaming tokens, **kill from terminal 2**:

```bash
pkill -SIGKILL -f 'go run ./durable_agent/restate|go-build/.*/restate'
```

**Terminal 2 — copy the state file somewhere safe** (do not skip this):

```bash
cat /tmp/durable_agent_restate_runstate.json
cp /tmp/durable_agent_restate_runstate.json /tmp/stale_restate_runstate.json
```

**Step B — wait until Restate finishes the invocation**

Do **not** restart the agent yet. Wait until the long reply is done (often 30–90+ seconds depending on the model). Optional checks:

- Restate Admin UI: [http://localhost:9070](http://localhost:9070) — invocation status completed
- Or simply wait longer than a typical essay/travel-plan reply

**Step C — restore the stale state file and restart**

```bash
cp /tmp/stale_restate_runstate.json /tmp/durable_agent_restate_runstate.json
go run ./durable_agent/restate
```

On startup:

```text
[reconnect] found saved run state:
  run_id : <uuid>
  offset : 10
  prompt : "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
Reconnect from last offset? [y/n]>
```

Type `y`.

**Expected behavior — run already completed:**

```text
[reconnect] reconnecting run_id=<uuid> from offset=10
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."

[reconnect] the run completed successfully while you were disconnected.
[reconnect] the response was generated, but streaming events are no longer available.
[reconnect] if conversation history is configured, the response is already saved —
[reconnect] start a new turn to continue. otherwise, start a new run.
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
```

The state file is cleared. You then get a normal `you>` prompt — type a **new** message to start a fresh run (this example does not wire multi-turn conversation history, so the completed reply is not shown again).

**Alternate (simpler) check — no stale file:**

1. Run a short prompt to completion (`--- stream end ---`).
2. Confirm state is gone: `ls /tmp/durable_agent_restate_runstate.json` → No such file.
3. Restart `go run ./durable_agent/restate` — no reconnect prompt; just `you>`.

**Learn:** Durability means Restate finished the work even if the client was gone. What you lose after completion is only the **streaming replay** for that run ID — not the ability to start a new turn.

---

### 5 — Restate not running

**Stop Restate, then start the agent:**

```bash
task infra:restate:down
go run ./durable_agent/restate "Hello"
```

**Expected behavior:** agent creation or the first stream fails with a connect/start error (Restate ingress unreachable).

Bring Restate back:

```bash
task infra:restate:up && task infra:restate:wait
```

Then retry the prompt.

---

### 6 — Docker networking (`DeploymentURL`)

If Restate runs in Docker and the agent runs on the host, registration can fail unless Restate can call back into the embedded SDK endpoint.

**Terminal 1 — start with an explicit deployment URL:**

```bash
RESTATE_DEPLOYMENT_URL=http://host.docker.internal:9080 go run ./durable_agent/restate
```

```text
you> Hello from Restate durable agent!
```

You should get a normal stream (`--- stream start ---` … `--- stream end ---`). If registration previously failed without `RESTATE_DEPLOYMENT_URL`, this is the fix for local Docker + host-process labs.

See [restate-setup.md](../../../restate-setup.md).

## Notes

- Topology is Restate ingress + embedded SDK endpoint in this process (no separate `NewAgentWorker`).
- Approvals (if triggered) use stream `Approve` the same as on Temporal.
- This lab does not wire multi-turn conversation history.
- Focused reconnect demo (Temporal or Restate): [`../../agent_with_reconnect/`](../../agent_with_reconnect/).

