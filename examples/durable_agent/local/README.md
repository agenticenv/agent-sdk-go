# durable_agent / local

Zero-infrastructure durability lab on the **local runtime**. Local is durable **by
default** via [durable-go](https://github.com/agenticenv/durable-go) — no external
server, no client/worker split, nothing to install. Every LLM call and tool execution
is journaled to disk under `./agent_data/local-durable-agent`. Kill the agent process
mid-stream, restart it from the **same working directory**, and reconnect with
`GetAgentStream` — durable-go replays each already-completed step from the journal as
one coalesced message, then continues live once reconnect reaches the in-flight step.

> Unlike Temporal, local's `Events` does not support `WithOffset(n)` for `n > 0` —
> there is no per-token durable log to seek into, only step-level replay. This example
> never calls `WithOffset`; the saved state file only needs a `run_id`.

State file: `/tmp/durable_agent_local_runstate.json`.

For the Temporal split worker/agent lab, see [`../temporal/`](../temporal/). For the
Restate single-process lab, see [`../restate/`](../restate/).

## Prerequisites

1. **Configuration** — `examples/README.md` [Configuration](../../README.md#configuration) (an LLM provider API key).
2. **Working directory** — run commands from `examples/`, and always restart from the
   same directory so the agent finds its journal at `./agent_data/local-durable-agent`.

That's it — no server to start, no `task infra:*` step.

## Quick start

```bash
# From examples/
go run ./durable_agent/local "Hello from the local durable agent!"
```

Interactive REPL (no args):

```bash
go run ./durable_agent/local
```

Type prompts at `you>`. Type `exit` / `quit` / `bye` to stop.

## Scenarios to try (durability)

Single process, no separate server. Use **two terminals** when you kill/restart the
agent (`terminal 1` = agent REPL, `terminal 2` = kill / `cat` / copy commands).

> Run all commands from the `examples/` directory — the journal directory
> (`./agent_data/local-durable-agent`) is relative to the working directory the
> process starts in.
>
> **Reconnect requires a live run.** Once the run finishes (or is cancelled),
> streaming events are no longer available for replay. If you restart after the run
> is already terminal, `GetAgentStream` returns `ErrRunAlreadyCompleted` and the agent
> asks you to start a new turn. Timing matters for scenario 2: reconnect while the run
> is still executing (a hard kill leaves it live). Scenarios 3 and 4 deliberately
> exercise the terminal-run path instead — for different reasons (scenario 3: graceful
> `Close` cancels it; scenario 4: it's allowed to actually finish).
>
> **Clean up between scenarios** (optional — clears the journal, a leftover state
> file, and any stray agent process):
>
> ```bash
> pkill -SIGKILL -f 'go run ./durable_agent/local|go-build/.*/local' 2>/dev/null
> rm -f /tmp/durable_agent_local_runstate.json
> rm -rf ./agent_data/local-durable-agent
> true
> ```
>
> **LLM reply text varies by model and run.** Labels below: **Expected startup
> output** (fixed banners), **Sample response shape** (structure only), **Expected
> behavior**.

---

### 1 — Happy path

**Terminal 1 — start the agent:**

```bash
go run ./durable_agent/local
```

Expected startup output:

```text
=== durable_agent/local interactive stream ===
Durability: ON (default) — steps are journaled under ./agent_data/local-durable-agent.
No external server required.
Kill -9 this process mid-stream, then restart (same working directory) to reconnect mid-run.
(Ctrl+C is a graceful Close — it cancels the run instead of leaving it resumable; see README scenario 3.)
Type 'exit' or 'quit' or 'bye' to stop.

you>
```

**Terminal 1 — type a short prompt:**

```text
Hello from the local durable agent!
```

Sample response shape (LLM text varies):

```text
[run_id] <uuid>
--- stream start ---
<assistant reply>
--- stream end ---

you>
```

Optional: `ls ./agent_data/local-durable-agent` — durable-go's journal files are there.

Type `bye` when finished, or leave the REPL open for the next scenario.

---

### 2 — Kill agent mid-stream, reconnect (crash)

This scenario shows `GetAgentStream`: the agent process is killed while streaming,
the journal on disk survives, and a restart resumes the same run.

> **Timing is critical.** Use a **long prompt** so the LLM call takes several
> seconds — enough time to kill, check the state file, and restart before the run
> finishes. If you wait too long, you get the "already completed" path (that is
> scenario 4).

**Terminal 1 — start the agent and send a long prompt:**

```bash
go run ./durable_agent/local
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

While tokens are streaming (within the first few seconds), **kill the agent from
terminal 2**:

```bash
pkill -SIGKILL -f 'go run ./durable_agent/local|go-build/.*/local'
```

Terminal 1 exits immediately (no cleanup) — simulating a crash. **Nothing else needs
to keep running** — the journal is just a directory on disk.

**Terminal 2 — confirm the state file was saved:**

```bash
cat /tmp/durable_agent_local_runstate.json
```

```json
{"run_id":"<uuid>","prompt":"Write a detailed day-by-day travel plan for a 7-day trip to Japan."}
```

**Terminal 1 — restart quickly, from the same directory** (while the run is still
executing on the journal's timeline):

```bash
go run ./durable_agent/local
```

On startup the saved state is detected:

```text
[reconnect] found saved run state:
  run_id : <uuid>
  prompt : "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
Reconnect? [y/n]>
```

Type `y`. The agent reconnects and replays the run's step history from the
journal — already-completed steps print as **one coalesced message each**
(`[step_replayed]` — step granularity, not the original token-by-token stream), then
live tokens resume once reconnect catches up to the in-flight step:

```text
[reconnect] reconnecting run_id=<uuid>
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."

--- stream resumed ---
[step_replayed] step=... status=completed — a previously completed step replayed as one coalesced message, not the original tokens
...Day 7: Farewell day in Kyoto...
--- stream end ---
```

The state file is cleared on `RUN_FINISHED`. The REPL then continues normally.

---

### 3 — Graceful Ctrl+C mid-stream — this *cancels* the run (not the same as a crash!)

This scenario is the opposite lesson from scenario 2. It's tempting to assume Ctrl+C
and `pkill -SIGKILL` behave the same because both "kill the process" — **they do
not**, on local. `a.Close()` (which this example's signal handler calls on Ctrl+C, for
graceful cleanup — flushing OTLP exporters, etc.) closes the durable-go engine it
owns, which **cancels every in-flight run on that engine** and waits for the
cancellation to be journaled before `Close()` returns. That run becomes terminal — not
resumable — exactly as if it had finished. A hard kill (`kill -9` / `pkill -SIGKILL`)
skips this shutdown path entirely, which is *why* scenario 2's resume works and this
one doesn't.

> This is specific to local. On Temporal/Restate, closing the client process never
> touches the server-side run — see [Durable Execution](/advanced/durable-execution).

**Terminal 1 — start the agent and send a long prompt:**

```bash
go run ./durable_agent/local
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
durable_agent/local stopped.
```

(If shutdown hangs, press Ctrl+C a second time to force exit — but it shouldn't:
`Close` waits for the cancellation to finish journaling, which is fast.)

**Terminal 1 — restart from the same directory:**

```bash
go run ./durable_agent/local
```

```text
[reconnect] found saved run state:
  run_id : <uuid>
  prompt : "Write a detailed essay about durable execution and why crash recovery matters."
Reconnect? [y/n]>
```

Type `y`.

**Expected behavior — the run is already terminal (cancelled by `Close`), not live:**

```text
[reconnect] reconnecting run_id=<uuid>
[reconnect] original prompt: "Write a detailed essay about durable execution and why crash recovery matters."

[reconnect] the run completed successfully while you were disconnected.
[reconnect] the response was generated, but streaming events are no longer available.
[reconnect] if conversation history is configured, the response is already saved —
[reconnect] start a new turn to continue. otherwise, start a new run.
[reconnect] original prompt: "Write a detailed essay about durable execution and why crash recovery matters."
```

(The message text says "completed successfully" because the SDK's `ErrRunAlreadyCompleted`
covers every terminal outcome — success, failure, and cancellation alike; it does not
distinguish which one happened. The run was actually **cancelled**, not completed.)

**Learn:** graceful shutdown (`Close`) and a crash are **not equivalent** for local
durability. Durability protects against unclean process death — it is not a mechanism
for "pause and resume later" across your own planned shutdowns.

---

### 4 — Restart after the run already finished (normally, not cancelled)

Scenario 3 already showed one way to hit `ErrRunAlreadyCompleted` (cancellation via
`Close`). This scenario hits the same error via the more obvious path — letting a run
**actually finish** — while also demonstrating a local-specific quirk: since there is
no server, nothing finishes an abandoned run for you. You have to reconnect once to
drive it to completion yourself.

> **Local has no separate server**, unlike Temporal (a worker keeps polling the task
> queue) or Restate (the durable invocation keeps going in Restate's server). Nothing
> drives an abandoned local run forward on its own — a run you kill mid-stream stays
> parked at `Running` in the journal until *some* process reconnects to it and
> finishes driving it. So to observe the completed-run path here, you first have to
> let the run finish via one normal reconnect, then replay an *older*,
> already-superseded state file against that now-terminal run.

**Step A — create a mid-run state file (same start as scenario 2)**

**Terminal 1 — start and send a long prompt:**

```bash
go run ./durable_agent/local
```

```text
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

As soon as you see `[run_id]` / streaming tokens, **kill from terminal 2**:

```bash
pkill -SIGKILL -f 'go run ./durable_agent/local|go-build/.*/local'
```

**Terminal 2 — copy the state file somewhere safe** (do not skip this — this stale
copy is what step C replays):

```bash
cat /tmp/durable_agent_local_runstate.json
cp /tmp/durable_agent_local_runstate.json /tmp/stale_local_runstate.json
```

**Step B — reconnect once and let it actually finish this time**

```bash
go run ./durable_agent/local
```

```text
[reconnect] found saved run state: ...
Reconnect? [y/n]> y
```

This time **do not kill it again** — let `--- stream end ---` print normally. This is
the step that drives the run to a terminal state; the real state file is cleared
automatically on `RUN_FINISHED`.

**Step C — restore the stale (now-superseded) state file and restart**

```bash
cp /tmp/stale_local_runstate.json /tmp/durable_agent_local_runstate.json
go run ./durable_agent/local
```

On startup:

```text
[reconnect] found saved run state:
  run_id : <uuid>
  prompt : "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
Reconnect? [y/n]>
```

Type `y`.

**Expected behavior — run already completed:**

```text
[reconnect] reconnecting run_id=<uuid>
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."

[reconnect] the run completed successfully while you were disconnected.
[reconnect] the response was generated, but streaming events are no longer available.
[reconnect] if conversation history is configured, the response is already saved —
[reconnect] start a new turn to continue. otherwise, start a new run.
[reconnect] original prompt: "Write a detailed day-by-day travel plan for a 7-day trip to Japan."
```

The state file is cleared. You then get a normal `you>` prompt — type a **new**
message to start a fresh run (this example does not wire multi-turn conversation
history, so the completed reply is not shown again).

**Alternate (simpler) check — no stale file:**

1. Run a short prompt to completion (`--- stream end ---`).
2. Confirm state is gone: `ls /tmp/durable_agent_local_runstate.json` → No such file.
3. Restart `go run ./durable_agent/local` — no reconnect prompt; just `you>`.

**Learn:** Durability means the journal captured the work even though the client that
started it is gone. What you lose after completion is only the **streaming replay**
for that run ID — not the ability to start a new turn.

---

### 5 — `DURABILITY=off` — the opt-out contrast

This scenario shows what you're actually opting *out of* by disabling durability. With
`local.DurabilityOff()`, execution is pure in-memory — no journal, no reconnect, ever.

**Terminal 1 — start with durability off and send a long prompt:**

```bash
DURABILITY=off go run ./durable_agent/local
```

Expected startup output:

```text
=== durable_agent/local interactive stream ===
Durability: OFF (DURABILITY=off) — pure in-memory, no journal, no reconnect after a crash.
Kill -9 this process mid-stream, then restart (same working directory) to reconnect mid-run.
(Ctrl+C is a graceful Close — it cancels the run instead of leaving it resumable; see README scenario 3.)
Type 'exit' or 'quit' or 'bye' to stop.

you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

While tokens are streaming, kill the agent from terminal 2 (same command as scenario 2).

**Terminal 1 — restart** (durability still off or on — doesn't matter, there is no
journal for this run either way):

```bash
go run ./durable_agent/local
```

```text
[reconnect] found saved run state: ...
Reconnect? [y/n]> y
[reconnect] reconnecting run_id=<uuid>
[reconnect] ErrStreamNotFound — no journal for this run.
[reconnect] this is expected if DURABILITY=off was set for the crashed process,
[reconnect] or if ./agent_data/local-durable-agent was deleted/moved before restart.
```

**Learn:** this is the entire value proposition of durable-by-default in one
contrast — with durability on (scenario 2), the same kill+restart sequence resumes
transparently; with it off, the work started by the killed process is simply gone.

---

## Production engine (caller-owned)

This lab uses the **SDK-built** engine (plaintext journal, ephemeral step-token key).
For production — at-rest encryption, journal MAC, or approval tokens that survive a
restart — build a [durable-go](https://github.com/agenticenv/durable-go) engine yourself
and pass it as `LocalConfig.Engine`. Same split as Temporal
`WithTemporalConfig` vs `WithTemporalClient`. You own `engine.Close()`; `a.Close()`
does not close a supplied engine.

Keys from the environment (hex). Do not turn codec/MAC on later against an existing
plaintext `dataDir` — use a new directory.

```go
payloadKey, err := hex.DecodeString(os.Getenv("DURABLE_PAYLOAD_KEY"))
codec, err := durable.NewAESGCMCodec(payloadKey)
macKey, err := hex.DecodeString(os.Getenv("DURABLE_JOURNAL_MAC_KEY"))
tokenKey, err := hex.DecodeString(os.Getenv("DURABLE_STEP_TOKEN_KEY"))

engine, err := durable.NewEngine(ctx, "./agent_data/my-agent",
    durable.WithPayloadCodec(codec),
    durable.WithJournalMACKey(macKey),
    durable.WithStepTokenKey(tokenKey),
    durable.WithAutoPurge(7*24*time.Hour),
)
defer engine.Close()

a, err := agent.NewAgent(
    agent.WithLLMClient(llmClient),
    local.WithLocalConfig(&local.LocalConfig{Engine: engine}),
)
```

Runnable example: [`../../agent_with_durable_engine/`](../../agent_with_durable_engine/).
Full options and caveats: [In-Process durability](https://docs.agenticenv.ai/runtimes/in-process#caller-owned-engine-payload-codec-journal-mac-step-token-key)
and [durable-go data privacy](https://github.com/agenticenv/durable-go#data-privacy--sensitive-payloads).

## Notes

- Topology is a single process, no server, no worker — `./agent_data/local-durable-agent`
  on disk *is* the durability mechanism.
- No `WithOffset`: local's `Events` only supports `fromOffset` 0. Reconnect always
  replays full step history (as coalesced `step_replayed` messages), then goes live —
  it is not seekable to an arbitrary token position the way Temporal's stream is.
- Approvals (if triggered) use stream `Approve` the same as on Temporal/Restate.
- This lab does not wire multi-turn conversation history.
- `./agent_data/` is git-ignored; delete it any time to reset — see "Clean up between
  scenarios" above.
- Focused reconnect demo (Temporal or Restate, token-level `WithOffset`): [`../../agent_with_reconnect/`](../../agent_with_reconnect/).
