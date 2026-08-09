# durable_agent / restate

Single-process durability lab on **Restate**. `NewAgent` embeds the Restate SDK endpoint. Events are durable via Restate; kill the agent mid-stream, keep Restate running, restart, and reconnect with `GetAgentStream` + `WithOffset`.

State file: `/tmp/durable_agent_restate_runstate.json`.

For the Temporal split worker/agent lab, see [`../temporal/`](../temporal/).

## Prerequisites

1. **Configuration** — [`examples/README.md` Configuration](../../README.md#configuration).
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

4. **Restate server**:

```bash
task infra:restate:up && task infra:restate:wait
```

Or see [`../../../restate-setup.md`](../../../restate-setup.md). Admin UI: http://localhost:9070.

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

## Scenarios

Use two terminals when killing/restarting the agent. Keep Restate up for the whole lab.

### 1 — Happy path

```bash
go run ./durable_agent/restate
```

At `you>`, send a short prompt. You should see stream events and `RUN_FINISHED`. Restate Admin UI can show the invocation.

### 2 — Kill agent mid-stream, reconnect

1. Start the agent and send a **long** prompt (e.g. “Write a detailed essay about durable systems…”).
2. While tokens stream, kill the process:

```bash
pkill -SIGKILL -f 'go run ./durable_agent/restate|go-build/.*/restate'
```

3. Confirm state was saved:

```bash
cat /tmp/durable_agent_restate_runstate.json
```

4. Restart **while Restate still has the live invocation**:

```bash
go run ./durable_agent/restate
```

5. Answer `y` to reconnect. You should resume from the saved offset (already-seen events skipped).

> Reconnect only works while the Restate invocation is still live. If it finishes before you restart, `GetAgentStream` returns `ErrRunAlreadyCompleted` — clear state and start a new prompt.

### 3 — Graceful Ctrl+C then restart

Same as scenario 2, but stop with Ctrl+C instead of `pkill`. Restate keeps the invocation; restart and reconnect if the run is still live.

### 4 — Restart after the run already finished

1. Complete a short prompt so the run finishes (state file cleared on `RUN_FINISHED`).
2. Or kill after completion / wait until Restate finishes, then restart with a stale state file if you saved one manually.
3. On reconnect, expect `ErrRunAlreadyCompleted` (or no saved state) — start a new turn.

### 5 — Restate not running

```bash
task infra:restate:down
go run ./durable_agent/restate "Hello"
```

Expect a start/connect error. Bring Restate back with `task infra:restate:up && task infra:restate:wait`.

### 6 — Docker networking (`DeploymentURL`)

If Restate is in Docker and the agent on the host, registration may fail unless Restate can reach the SDK endpoint:

```bash
RESTATE_DEPLOYMENT_URL=http://host.docker.internal:9080 go run ./durable_agent/restate
```

See [restate-setup.md](../../../restate-setup.md).

## Notes

- Topology is Restate ingress + embedded SDK endpoint in this process.
- Approvals (if triggered) use stream `Approve` the same as on Temporal.
- This lab does not wire multi-turn conversation history.
- Focused reconnect demo (Temporal or Restate): [`../../agent_with_reconnect/`](../../agent_with_reconnect/).
