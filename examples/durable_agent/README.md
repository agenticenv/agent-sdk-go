# durable_agent

Separate **agent** and **worker** processes: `DisableLocalWorker` / `EnableRemoteWorkers` on the agent, `NewAgentWorker` on the worker, shared options in [`opts/opts.go`](opts/opts.go). The agent uses **`Stream`** with `WithStream(true)` and prints events (tokens, tools, approvals, `complete`).

```bash
# From examples/ (after cp env.sample .env and Temporal is up)
go run ./durable_agent/worker  # Run on terminal 1
go run ./durable_agent/agent "Hello from remote agent!" # Run on terminal 2
```

## Scenarios to try (durability)

Use two terminals: **terminal 1 = worker**, **terminal 2 = agent**. Temporal keeps workflow state; these exercises show how the split behaves when processes or connectivity change.

> Run all commands from the `examples/` directory.
> ⚠️ **Start the worker before typing a prompt in the agent REPL.** The worker check
> runs at prompt submission time, not at agent start. The wait duration is controlled
> by your `ctx` deadline or `WithTimeout` config — the default is **5 minutes**
> (**`AgentModeInteractive`**). Each scenario below tells you exactly when to start
> each process.

---

### 1. Baseline — worker first

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Expected output:

```text
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
```

**Terminal 2 — start the agent:**

```bash
go run ./durable_agent/agent
```

Expected output:

```text
=== durable_agent interactive stream ===
Events arrive via the event workflow (UpdateWorkflow path).
Simulate scenarios: kill the worker or this process mid-run, then restart.
Type 'exit' or 'quit' or 'bye' to stop.

you>
```

**Terminal 2 — type a prompt:**

```text
Hello from remote agent!
```

Expected output:

```text
--- stream start ---
Hello! How can I help you today?
[complete]
[usage] prompt=10 completion=12 total=22
--- stream end ---

you>
```

Confirms the remote worker path works end to end.

---

### 2. Agent without worker (intentional timeout)

**Terminal 2 — start the agent with no worker running:**

```bash
go run ./durable_agent/agent
```

Wait for the REPL prompt, then type:

```text
Hello from remote agent!
```

> **Note:** This scenario intentionally lets the worker-wait timeout expire. The
> agent submits the workflow to Temporal then checks for available workers — if none
> are found within the check window it returns a clear error. The default is
> **5 minutes** (**`AgentModeInteractive`**). For **`AgentModeAutonomous`** the worker
> check is skipped entirely and the workflow queues in Temporal until a worker
> comes up. After the error, start the worker and resend — that is the expected flow.

Expected output (after timeout):

```text
--- stream start ---
[error] no worker available: timed out waiting for workers on task queue "agent"
--- stream end ---

you>
```

**Terminal 1 — now start the worker:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — resend the prompt:**

```text
Hello from remote agent!
```

Expected output:

```text
--- stream start ---
Hello! How can I help you today?
[complete]
[usage] prompt=10 completion=12 total=22
--- stream end ---

you>
```

The error was intentional — for interactive agents, failing fast is better than hanging indefinitely.

---

### 3a. Kill worker between LLM rounds — graceful stop (planned shutdown)

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Wait for:

```
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
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

```
^C
Shutting down agent worker...
```

**Terminal 1 — restart the worker:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — send another prompt:**

```
you> Hello again!
```

Expected output:

```
--- stream start ---
Hello again! How can I help?
[complete]
[usage] prompt=22 completion=8 total=30
--- stream end ---

you>
```

Simulates a planned worker shutdown — deploy, upgrade, or config change.
Completed activity results are already recorded in Temporal workflow history — the restarted worker does not re-execute them.

---

### 3b. Kill worker between LLM rounds — crash (unplanned shutdown)

**Terminal 1 — start the worker:**

```bash
go run ./durable_agent/worker
```

Wait for:

```
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
```

**Terminal 2 — start the agent and send a prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Hello from remote agent!
```

Wait for `--- stream end ---`.

**Terminal 3 — find the worker process ID and kill it:**

```bash
pgrep -f "durable_agent/worker"
```

```bash
kill -9 <pid>
```

Terminal 1 exits immediately with no cleanup — simulating a real worker crash.

**Terminal 1 — restart the worker:**

```bash
go run ./durable_agent/worker
```

**Terminal 2 — send another prompt:**

```
you> Hello again!
```

Expected output:

```
--- stream start ---
Hello again! How can I help?
[complete]
[usage] prompt=22 completion=8 total=30
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
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
```

**Terminal 2 — start the agent and send a longer prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

Watch tokens streaming in terminal 2. While streaming is active, **Ctrl+C the
worker in terminal 1 and do not restart it:**

```
^C
Shutting down agent worker...
```

Expected output in terminal 2 (stream pauses, no immediate error):

```
--- stream start ---
Here is a detailed day-by-day travel plan for Japan...

Day 1: Arrive in Tokyo
```

The stream goes silent — Temporal is waiting for a worker to poll and resume
the in-flight activity. No worker is available so no events are sent to the
agent stream. After the agent timeout (default **5 minutes**) the error
surfaces:

```
[error] deadline exceeded: no worker resumed the workflow within the timeout
--- stream end ---

you>
```

This confirms the agent fails clearly on timeout rather than hanging
indefinitely. Start the worker again to resume normal operation:

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
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
```

**Terminal 2 — start the agent and send a longer prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

Watch tokens streaming in terminal 2. While streaming is active, **Ctrl+C the
worker in terminal 1:**

```
^C
Shutting down agent worker...
```

Expected output in terminal 2 (stream pauses, no immediate error):

```
--- stream start ---
Here is a detailed day-by-day travel plan for Japan...

Day 1: Arrive in Tokyo
```

**Terminal 1 — restart the worker before the timeout (within 5 minutes):**

```bash
go run ./durable_agent/worker
```

Temporal reschedules the in-flight LLM activity on the restarted worker. The LLM call reruns (activities are retried, not replayed) and the stream resumes automatically in terminal 2:

> **Note:** When the worker restarts and the LLM activity reruns, you may see
> overlapping or repeated tokens in the stream — this is expected. The activity
> retries from the beginning so streaming output may duplicate content already
> seen before the worker stopped. The final stored response in conversation
> history is always the single complete result, not the duplicated stream chunks.

```
Day 1: Arrive in Tokyo — Start at Shinjuku...
Day 2: Explore Asakusa and Ueno...
...
[complete]
[usage] prompt=20 completion=300 total=320
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
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
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

Expected output:

```
--- stream start ---
Hello again! How can I help?
[complete]
[usage] prompt=22 completion=8 total=30
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
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
```

**Terminal 2 — start the agent and send a prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Hello from remote agent!
```

Wait for `--- stream end ---`.

**Terminal 3 — find the agent process ID and kill it:**

```bash
pgrep -f "durable_agent/agent"
```

```bash
kill -9 <pid>
```

Terminal 2 exits immediately with no cleanup — simulating a real crash (OOM, hardware failure, or unhandled panic).

**Terminal 2 — start the agent again:**

```bash
go run ./durable_agent/agent
```

```
you> Hello again!
```

Expected output:

```
--- stream start ---
Hello again! How can I help?
[complete]
[usage] prompt=22 completion=8 total=30
--- stream end ---

you>
```

Simulates an unexpected crash. The worker never stopped — it continues polling the same task queue. The new agent process connects to the same Temporal namespace and resumes work immediately. No state is lost.

---

### 5c. Agent crash mid-LLM call (workflow completes without user)

**Terminal 1 — start the worker and leave it running:**

```bash
go run ./durable_agent/worker
```

Wait for:

```
Agent worker starting on task queue "agent". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
```

**Terminal 2 — start the agent and send a longer prompt:**

```bash
go run ./durable_agent/agent
```

```
you> Write a detailed day-by-day travel plan for a 7-day trip to Japan.
```

Watch tokens streaming in terminal 2. While streaming is active, **find the agent process ID and kill it from terminal 3:**

```bash
pgrep -f "durable_agent/agent"
```

```bash
kill -9 <pid>
```

Terminal 2 exits immediately — the user sees no response.

**Terminal 1 — the worker keeps running.** Temporal holds the workflow state and the worker completes the LLM call and finishes the run — even though the agent process is gone.

**Terminal 2 — start the agent again and send a follow-up:**

```bash
go run ./durable_agent/agent
```

```
you> What was the first destination in that plan?
```

Expected output without conversation history (default `durable_agent`):

```
--- stream start ---
I don't have context of a previous plan. Could you clarify?
[complete]
--- stream end ---

you>
```

> **Note:** The `durable_agent` example does not wire up conversation history —
> the completed response was stored in Temporal workflow history but the restarted
> agent has no memory of the previous session. To see the full durability story
> where the completed response is visible after agent restart, use
> **[Agent Chat](https://github.com/agenticenv/agent-chat)** — the web UI stores
> conversation history so the result appears on reconnect even if the agent
> crashed mid-stream. This is the recommended approach for production interactive
> apps where users expect continuity across restarts.

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

**Ctrl+C one worker** mid-session (either terminal 1 or 3), then send another
prompt:

```text
you> Still working?
```

Expected output:

```text
--- stream start ---
Yes, still here! How can I help?
[complete]
[usage] prompt=30 completion=8 total=38
--- stream end ---

you>
```

Both workers poll the same task queue — Temporal distributes the load automatically. Stopping one worker mid-session does not drop a run.

---

### 7. Task queue mismatch

Open `durable_agent/worker/main.go` and temporarily change the task queue name to something different from the agent's task queue (e.g. `"wrong-queue"`), then start the worker:

**Terminal 1:**

```bash
go run ./durable_agent/worker
```

Expected output:

```text
Agent worker starting on task queue "wrong-queue". Run this before the agent.
Agent worker running. Press Ctrl+C to stop.
```

**Terminal 2:**

```bash
go run ./durable_agent/agent
```

```text
you> Hello from remote agent!
```

Expected output (after timeout):

```text
--- stream start ---
[error] no worker available: timed out waiting for workers on task queue "agent"
--- stream end ---

you>
```

Misconfiguration surfaces clearly rather than silently corrupting state. Revert the task queue name in `durable_agent/worker/main.go` and restart both processes to recover.

> **Tip:** For **`AgentModeAutonomous`**, the worker check is skipped entirely — a
> task queue mismatch will not error immediately but will cause the workflow to
> queue in Temporal until the agent timeout hits. Always verify task queue names
> match across agent and worker config before deploying.
