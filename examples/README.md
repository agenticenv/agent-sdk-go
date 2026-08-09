# Examples

These programs exercise **agent-sdk-go** (`github.com/agenticenv/agent-sdk-go`). By default examples run on the **local** runtime (in-process, no external services). Set `AGENT_RUNTIME=temporal` or `AGENT_RUNTIME=restate` in `.env` for durable execution.

## Configuration

Set this up once before running any example. Per-example READMEs only list extra services or variables beyond this base.

### Flow

1. **`cd examples`** — every `go run` and `task` command assumes this directory.
2. **Create `examples/.env`** — gitignored file for your API key and any overrides (see below).
3. **Run an example** — e.g. `go run ./simple_agent "Hello"`. On startup, [`config.go`](config.go) loads env vars and each program builds its agent from that config.
4. **Optional infra** — if the example needs Redis, Temporal, Restate, Weaviate, etc., start it first with `task infra:*:up` ([Setup](#setup)), then `go run`.

You do not copy or edit `.env.defaults` for normal use — it supplies committed defaults (ports, stdio MCP command, Temporal/Restate hosts, etc.) and is loaded automatically before your `.env`.

### Requirements

| Requirement | Notes |
|---|---|
| **Go 1.26+** | `go version` |
| **LLM API key** | `LLM_APIKEY` in `examples/.env` |
| **Working directory** | Run commands from **`examples/`** |

Supported providers: `openai`, `anthropic`, `gemini` — set `LLM_PROVIDER` and `LLM_MODEL` in `.env` (defaults in [`.env.defaults`](.env.defaults)).

### Environment files

Load order (later wins):

| Layer | File / source | Purpose |
|---|---|---|
| 1 | [`.env.defaults`](.env.defaults) | Committed defaults — loaded automatically; do not put secrets here |
| 2 | [`.env`](.env) | Your secrets and local overrides (gitignored) |
| 3 | Process environment | `export`, root `Taskfile.yml` dotenv, or CI — highest precedence |

Create `.env` once with at least your LLM key:

```bash
cd examples
cat >> .env <<'EOF'
LLM_APIKEY=your-key-here
LLM_PROVIDER=openai
LLM_MODEL=gpt-4o
EOF
```

Never commit API keys. Override any default the same way — e.g. `AGENT_RUNTIME=temporal`, `AGENT_RUNTIME=restate`, remote `A2A_URL`, or `MCP_TRANSPORT=streamable_http` + `MCP_STREAMABLE_HTTP_URL`.

### Optional dependencies

| Dependency | When needed |
|---|---|
| **Docker + Compose** | Weaviate, pgvector, Redis, Temporal, Restate, OTLP collector |
| **[Task](https://taskfile.dev)** | `task infra:*:up` from `examples/` — see [Setup](#setup) |
| **Node.js** | MCP stdio server (`npx`), AG-UI Next.js UI |
| **Temporal server** | When `AGENT_RUNTIME=temporal` — see [Runtime](#runtime) |
| **Restate server** | When `AGENT_RUNTIME=restate` — see [Runtime](#runtime) |
| **`EMBEDDING_OPENAI_APIKEY`** | pgvector and some memory/retriever examples |

Full variable list: [Env vars](#env-vars) below and [`.env.defaults`](.env.defaults).

## Runtime

| Mode | How to enable | Requirement |
|------|--------------|-------------|
| `local` (default) | `AGENT_RUNTIME=local` (or unset) | Nothing — runs in-process |
| `temporal` | `AGENT_RUNTIME=temporal` | **`task infra:temporal:up`** + **`infra:temporal:wait`** from `examples/`, or **[Temporal setup](../temporal-setup.md)** |
| `restate` | `AGENT_RUNTIME=restate` | **`task infra:restate:up`** + **`infra:restate:wait`** from `examples/`, or **[Restate setup](../restate-setup.md)** |

When using Temporal the examples read `TEMPORAL_HOST`, `TEMPORAL_PORT`, and `TEMPORAL_NAMESPACE` from `.env` (default: localhost, 7233, default).

When using Restate the examples read `RESTATE_INGRESS_URL`, `RESTATE_ADMIN_URL`, `RESTATE_ENDPOINT_LISTEN_ADDRESS`, and optional `RESTATE_DEPLOYMENT_URL` / `RESTATE_AUTH_KEY` from `.env` (defaults: ingress `http://localhost:8080`, admin `http://localhost:9070`, listen `:9080`). If Restate runs in Docker and the agent on the host, set `RESTATE_DEPLOYMENT_URL=http://host.docker.internal:9080`. Example Weaviate is published on host **8081** (`WEAVIATE_HOST=localhost:8081`) so it does not collide with Restate ingress on **8080**.

## Examples overview

From **`examples/`**, use [Task](https://taskfile.dev) and [`Taskfile.yml`](Taskfile.yml) for infra in the table below, then **`go run ./<example>`**. Install **`task`**, infra targets, and batch runs: [Setup](#setup). Third-party MCP/A2A servers stay manual — see each example’s README.

### Works with local, Temporal, and Restate

These examples run with `AGENT_RUNTIME=local` (default), `AGENT_RUNTIME=temporal`, or `AGENT_RUNTIME=restate` via `config.RuntimeOption`.

**Temporal:** set `AGENT_RUNTIME=temporal` in `.env`, then run **`task infra:temporal:up`** and **`task infra:temporal:wait`** before `go run` (for every row below, in addition to the infra in the third column).

**Restate:** set `AGENT_RUNTIME=restate` in `.env`, then run **`task infra:restate:up`** and **`task infra:restate:wait`** before `go run`. Restate embeds the SDK endpoint in the example process.

| Example | What it demonstrates | Infra (Task, from `examples/`) |
|---------|---------------------|--------------------------------|
| `simple_agent` | Minimal agent, no tools — system prompt, LLM client, single `Run()`; prints `AgentResponse.Usage` (token counts) when the provider reports them | — |
| `agent_with_conversation` | Redis conversation with `WithConversation(conversation.Config{...})` — multi-turn context, same `conversationID` for `Run` | `infra:redis:up` (or `infra:deps:up`) |
| `agent_with_tools/basic` | Built-in tools (echo, calculator, weather, wikipedia, search) with auto-approval | — |
| `agent_with_tools/approval` | Tools + `WithApprovalHandler` — user approves or rejects each tool run (`Run` only) | — |
| `agent_with_tools/authorizer` | Custom tool authorization via `interfaces.ToolAuthorizer` — denied calls surface as `tool_result` with `denied` status | — |
| `agent_with_tools/custom` | Custom tools via `WithTools` — implementing `interfaces.Tool` | — |
| `agent_with_tools/dynamic_registry` | Register a tool on a live agent between two runs — shows `ToolRegistry().Register` changing what the model can call without restarting | — |
| `agent_with_stream` | Streaming with `Stream` — **`TEXT_MESSAGE_*`**, **`TOOL_CALL_*`**, **`RUN_FINISHED`**; prints token usage from **`RUN_FINISHED`** result when present | — |
| `agent_with_agui` | Go **`POST /agui` SSE** + **Next.js + CopilotKit** ([`agent_with_agui/README.md`](agent_with_agui/README.md)) — agent server, then `ui/` dev server | UI manual (`npm run dev` in `ui/`) |
| `agent_with_stream_conversation` | Stream + conversation; avoid printing the same text twice (**`TEXT_MESSAGE_CONTENT`** deltas vs **`RUN_FINISHED`** body) | — |
| `agent_with_nonblocking_run` | Non-blocking `Run` — wait on `AgentRun.Done()`, then `Get()`; `WithApprovalHandler` for approvals | — |
| `agent_with_concurrent_runs` | Multiple `Run` calls in parallel on a **single** `Agent` instance — fan-out with `WaitGroup`, results via `Get` as they arrive | — |
| `multiple_agents` | Multiple agents in one process — sequential or concurrent | — |
| `agent_with_subagents` | Main agent + math specialist — `WithSubAgents`; prints **`STEP_STARTED` / `STEP_FINISHED`** (sub-agent name) around each child run when using `Stream` | — |
| `agent_with_json_response` | Structured LLM output — `WithResponseFormat` + `interfaces.JSONSchema` (JSON with schema; no tools) | — |
| `agent_with_reasoning` | Generic `interfaces.LLMReasoning` via `WithLLMSampling` — `Stream` to observe `thinking_delta` (e.g. Anthropic) | — |
| `agent_with_mcp_config` | MCP via `WithMCPConfig` — transport from env; **[README](agent_with_mcp_config/README.md)** | stdio: — (`.env.defaults`); remote MCP: manual |
| `agent_with_mcp_client` | Same via `mcpclient.NewClient` + `WithMCPClients` — **[README](agent_with_mcp_client/README.md)** | same as `mcp_config` |
| `agent_with_a2a_config` | Outbound A2A via `WithA2AConfig` — **`A2A_URL`**; **[README](agent_with_a2a_config/README.md)** | `infra:a2a:up` or external A2A (manual) |
| `agent_with_a2a_client` | Same env, explicit **`pkg/a2a/client`** | same as `a2a_config` |
| `agent_with_a2a_server` | **Inbound** A2A server — **`A2A_SERVER_*`**; **[README](agent_with_a2a_server/README.md)** | `go run` or `infra:a2a:up` |
| `agent_with_observability` | OTLP — **`config/`** vs **`objects/`**; **[README](agent_with_observability/README.md)** | `infra:lgtm:up` (or manual collector) |
| `agent_with_retriever` | **`weaviate/`** or **`pgvector/`**; **`RETRIEVER_MODE`** — **[README](agent_with_retriever/README.md)** | `infra:weaviate:up` or `infra:pgvector:up` |
| `agent_with_memory` | **`weaviate/`** or **`pgvector/`** — **[README](agent_with_memory/README.md)**; `MEMORY_STORE_MODE=always\|ondemand` | `infra:weaviate:up` or `infra:pgvector:up` |
| `agent_with_hooks` | All middleware hooks — PII scrubbing, retrieval filtering, memory tenant checks; **[README](agent_with_hooks/README.md)** | — |
| `agent_with_execution_config` | Execution config — `WithLLMExecutionConfig`, `WithToolExecutionConfig`, `WithSubAgentExecutionConfig`; SDK defaults and partial overrides | — |
| `agent_with_workflows` | Deterministic workflow execution via `run_workflow` tool — `WorkflowRunner` interface; `inprocess_runner.go` (default, no infra) and `temporal_runner.go` (`ORCHESTRATION_ENGINE=temporal`) | `infra:temporal:up`, `infra:temporal:wait` (Temporal engine only) |
| `agent_with_code_execution` | Sandboxed code execution via `execute_code` tool — `SandboxRuntime` interface; `local_runner.go` (default, needs Python/Node) and `docker_runner.go` (`SANDBOX_ENV=docker`) | Docker (Docker runner only) |

### Durable runtimes (Temporal or Restate)

Requires **`AGENT_RUNTIME=temporal`** or **`AGENT_RUNTIME=restate`** (not local — no stream offsets). Same reconnect APIs on both; single process by default.

| Example | What it demonstrates | Infra (Task, from `examples/`) |
|---------|---------------------|--------------------------------|
| `agent_with_reconnect` | **`GetAgentStream`** end to end — simulate subscriber crash, resume from offset — **[README](agent_with_reconnect/README.md)** | `infra:temporal:*` or `infra:restate:*` |

### Temporal only

Set **`AGENT_RUNTIME=temporal`**. Start **`task infra:temporal:up`** and **`task infra:temporal:wait`** before `go run`. These use Temporal-specific APIs (`WithTemporalClient`, `NewAgentWorker`, split-process labs). For Restate durability labs, see [Restate only](#restate-only) below.

| Example | What it demonstrates | Infra (Task, from `examples/`) |
|---------|---------------------|--------------------------------|
| `agent_with_temporal_client` | Caller-owned Temporal client — `temporal.WithTemporalClient`; TLS, API key, Cloud | `infra:temporal:up`, `infra:temporal:wait` |
| `agent_with_worker` | Agent and worker in **separate processes** — `DisableLocalWorker` + `NewAgentWorker`; **`Stream`**; events delivered via **Temporal Workflow Streams** | `infra:temporal:up`, `infra:temporal:wait` |
| `durable_agent/temporal` | Split-process durability — **`Stream`** with Temporal Workflow Streams; kill worker/agent mid-run and restart — **[README](durable_agent/temporal/README.md)** | `infra:temporal:up`, `infra:temporal:wait` |

### Restate only

Set **`AGENT_RUNTIME=restate`**. Start **`task infra:restate:up`** and **`task infra:restate:wait`** before `go run`.

| Example | What it demonstrates | Infra (Task, from `examples/`) |
|---------|---------------------|--------------------------------|
| `durable_agent/restate` | Single-process durability — embedded SDK endpoint; kill agent mid-run and reconnect — **[README](durable_agent/restate/README.md)** | `infra:restate:up`, `infra:restate:wait` |

Index: **[durable_agent/README.md](durable_agent/README.md)**.

## Setup

For **`.env`** and credentials, see [Configuration](#configuration) first. Add **`EMBEDDING_OPENAI_APIKEY`** there when running pgvector or embedding-backed memory/retriever examples.

**Task** — not installed by default; install via **[Task installation](https://taskfile.dev/installation/)** (platform-specific). Not needed for **`go run ./<example>`** when the overview table has no infra. Compose infra also needs **Docker**. From **`examples/`**: **`task infra:status`**, **`infra:deps:up`** / **`down`**, **`infra:*:up`** / **`down`**. From **repo root**: **`task examples:local`**, **`task examples:temporal`**, **`task examples:restate`**, **`task examples:all`**. Contributors: run **`task examples:all`** before any PR to catch regressions across local, Temporal, and Restate runtimes. New examples that can run non-interactively (one-shot, no REPL) should be listed in **`taskfiles/examples.yml`** (`EXAMPLES`, `EXAMPLES_WITH_PROMPTS`, `EXAMPLES_TEMPORAL`, or `EXAMPLES_RESTATE` as appropriate). **`task --dry`** only prints commands (no report file). To preview the report layout without running examples or infra, use **`task examples:local:plan`**, **`task examples:temporal:plan`**, **`task examples:restate:plan`**, or **`task examples:all:plan`**.

## Run examples

### Minimal agent (no tools)

```bash
go run ./simple_agent "Hello, what can you do?"
```

### Agent with conversation (multi-turn)

Uses Redis (`REDIS_ADDR`, default `localhost:6379`). Start Redis first: `task infra:redis:up`. Run **interactive mode** (no args) for multi-turn in one process—history is shared across turns. With args, runs a single turn (useful for testing).

```bash
task infra:redis:up
# Interactive: type prompts, get responses; history shared. Type 'exit' to end.
go run ./agent_with_conversation

# Single turn (new process each run; no shared history)
go run ./agent_with_conversation "Hello, remember I'm Alice"
```

### Agent with tools

```bash
go run ./agent_with_tools/basic "What's the weather in Tokyo?"
go run ./agent_with_tools/approval "What is 15 + 27?"
go run ./agent_with_tools/authorizer "Get the protected note for roadmap."
go run ./agent_with_tools/custom "Reverse 'hello world'"
go run ./agent_with_tools/dynamic_registry
```

### Agent with workflows

```bash
# in-process runner (default — no infrastructure needed)
go run ./agent_with_workflows "Run the onboarding workflow for user Alice"

# Temporal runner — durable execution
task infra:temporal:up && task infra:temporal:wait
ORCHESTRATION_ENGINE=temporal go run ./agent_with_workflows "Run the onboarding workflow for user Alice"
```

### Agent with code execution

```bash
# local sandbox (default — needs Python or Node installed)
go run ./agent_with_code_execution "Write a Python script that prints the first 10 Fibonacci numbers"

# Docker sandbox — isolated execution
SANDBOX_ENV=docker go run ./agent_with_code_execution "Write a Python script that prints the first 10 Fibonacci numbers"
```

### Agent with hooks

Middleware hooks across LLM, tools, retrieval, and memory. Hook activity is logged to stderr (`[hooks]` prefix).

```bash
go run ./agent_with_hooks
go run ./agent_with_hooks "My email is alice@example.com. What is the return policy?"
```

See **[agent_with_hooks/README.md](agent_with_hooks/README.md)**. When using **`AGENT_RUNTIME=temporal`**, register the same hook groups on both the agent starter and the worker. On Restate, hooks run in the process that serves the embedded endpoint.

### Streaming (partial content as tokens arrive)

```bash
go run ./agent_with_stream "What's the current time and what's 17 * 23?"
```

### AG-UI / CopilotKit (`agent_with_agui`)

Go SSE server + Next.js frontend. Two processes:

```bash
# Terminal 1: Go agent server (listens on :8787)
go run ./agent_with_agui/server

# Terminal 2: Next.js UI
cd agent_with_agui/ui && npm install && npm run dev
```

See **[agent_with_agui/README.md](agent_with_agui/README.md)** for curl testing and UI setup.

### Structured JSON response (`WithResponseFormat`)

```bash
go run ./agent_with_json_response
go run ./agent_with_json_response "What is the capital of Japan?"
```

### Reasoning / thinking (`WithLLMSampling` + `LLMReasoning`)

```bash
go run ./agent_with_reasoning
go run ./agent_with_reasoning "Why is the sky blue? One short paragraph."
```

### Streaming + conversation (event handling pattern)

```bash
go run ./agent_with_stream_conversation
go run ./agent_with_stream_conversation "What is 5 * 8?"
```

### Sub-agents (main agent + specialist)

```bash
go run ./agent_with_subagents "What is 987 times 654?"
```

### Non-blocking Run + concurrent runs + multiple agents

```bash
go run ./agent_with_nonblocking_run "What is 15 + 27?"
go run ./agent_with_concurrent_runs
go run ./agent_with_concurrent_runs "Who wrote Hamlet?" "What is sqrt(144)?" "Name a primary color."
go run ./multiple_agents "What is 7 times 8?"
go run ./multiple_agents concurrent "What is 7 times 8?"
```

### MCP (`agent_with_mcp_config`, `agent_with_mcp_client`)

Same **`MCP_*`** env (see **`.env.defaults`**); differs only in **`WithMCPConfig`** vs **`mcpclient.NewClient`** + **`WithMCPClients`**.

```bash
go run ./agent_with_mcp_config
go run ./agent_with_mcp_config "List tools you can call."
go run ./agent_with_mcp_client
go run ./agent_with_mcp_client "List tools you can call."
```

**Configure transports, test against real MCP servers:** **[agent_with_mcp_config/README.md](agent_with_mcp_config/README.md)**.

### A2A client (`agent_with_a2a_config`, `agent_with_a2a_client`)

Outbound A2A tools — set **`A2A_URL`** (and optional **`A2A_*`** in **`.env.defaults`**).

```bash
go run ./agent_with_a2a_config
go run ./agent_with_a2a_config "What tools do you have available?"
go run ./agent_with_a2a_client
go run ./agent_with_a2a_client "What tools do you have available?"
```

**Run a sample remote agent, curl checks:** **[agent_with_a2a_config/README.md](agent_with_a2a_config/README.md)**.

### A2A server (`agent_with_a2a_server`)

Inbound JSON-RPC server — **`A2A_SERVER_*`**, optional bearer tokens.

```bash
go run ./agent_with_a2a_server
```

**curl, `a2a` CLI, testing with `agent_with_a2a_config`:** **[agent_with_a2a_server/README.md](agent_with_a2a_server/README.md)**.

### Observability OTLP (`agent_with_observability`)

Requires a reachable OTLP **collector** (**`OTEL_EXPORTER_OTLP_ENDPOINT`**, typically **`localhost:4317`** for gRPC or **`localhost:4318`** for HTTP).

```bash
go run ./agent_with_observability/config/
go run ./agent_with_observability/objects/
```

Details and collector notes: **[agent_with_observability/README.md](agent_with_observability/README.md)**.

### Vector retriever (`agent_with_retriever`)

Requires a running vector store (Weaviate **or** Postgres with pgvector). Set backend-specific vars in **`.env.defaults`**.

```bash
# Weaviate (task infra:weaviate:up first; task infra:weaviate:down when done)
go run ./agent_with_retriever/weaviate "What is the return policy?"

# pgvector (task infra:pgvector:up first; task infra:pgvector:down when done)
go run ./agent_with_retriever/pgvector "What is the return policy?"

RETRIEVER_MODE=prefetch go run ./agent_with_retriever/weaviate "What are the return and shipping rules?"
```

Setup guides: **[agent_with_retriever/README.md](agent_with_retriever/README.md)**.

### Long-term memory (`agent_with_memory`)

Same vector infra as retriever (`task infra:weaviate:up` or `task infra:pgvector:up`). **Weaviate** uses always store (run-end extract); **pgvector** uses on-demand store (`save_memory`). No CLI args runs a two-turn demo (store, then recall).

```bash
go run ./agent_with_memory/weaviate
go run ./agent_with_memory/pgvector
```

Setup guide: **[agent_with_memory/README.md](agent_with_memory/README.md)**.

---

### Temporal-only examples

> These require `AGENT_RUNTIME=temporal` and a running Temporal server.

#### Caller-owned Temporal client

Creates and manages the Temporal client directly — for TLS, Temporal Cloud API keys, or custom connection options.

```bash
AGENT_RUNTIME=temporal go run ./agent_with_temporal_client "Hello, what can you do?"
```

#### Agent + worker in separate processes (`agent_with_worker`)

```bash
AGENT_RUNTIME=temporal go run ./agent_with_worker/worker    # terminal 1: worker
AGENT_RUNTIME=temporal go run ./agent_with_worker/agent "Hello from remote agent!"   # terminal 2: agent
```

#### Durable agent — Temporal (`durable_agent/temporal`)

```bash
AGENT_RUNTIME=temporal go run ./durable_agent/temporal/worker       # terminal 1
AGENT_RUNTIME=temporal go run ./durable_agent/temporal/agent "Hello from remote agent!"   # terminal 2
```

See **[durable_agent/temporal/README.md](durable_agent/temporal/README.md)**.

#### Durable agent — Restate (`durable_agent/restate`)

```bash
task infra:restate:up && task infra:restate:wait
AGENT_RUNTIME=restate go run ./durable_agent/restate "Hello from Restate durable agent!"
```

See **[durable_agent/restate/README.md](durable_agent/restate/README.md)**.

#### Reconnect — resume a stream from a saved offset (`agent_with_reconnect`)

Demonstrates `GetAgentStream`: the agent starts a stream, deliberately cancels it after the first text chunk (simulating a crash), then reconnects with `GetAgentStream(ctx, savedRunID)` and `Events(ctx, WithOffset(savedOffset))`. Works with **Temporal** and **Restate** (single process). See **[agent_with_reconnect/README.md](agent_with_reconnect/README.md)**.

```bash
# Temporal
task infra:temporal:up && task infra:temporal:wait
AGENT_RUNTIME=temporal go run ./agent_with_reconnect "What time is it?"

# Restate
task infra:restate:up && task infra:restate:wait
AGENT_RUNTIME=restate go run ./agent_with_reconnect "What time is it?"
```

---

## Logging

Examples send conversation (user prompt, assistant response) to **stdout** and SDK logs to **stderr**. By default SDK logging is off (`LOG_ENABLE=false` → `NoopLogger`); example `fmt`/`log` output is unchanged.

- **Enable SDK logs for debugging:** Set `LOG_ENABLE=true` and optionally `LOG_LEVEL`:
  ```bash
  LOG_ENABLE=true LOG_LEVEL=debug go run ./simple_agent "Hello, what can you do?"
  ```
- **Save logs to a file:**
  ```bash
  LOG_ENABLE=true LOG_LEVEL=info go run ./simple_agent "Hello" 2>debug.log
  ```

## Run output

All examples call [`shared.PrintRunFooters`](shared/utils.go) after each run. Set these in `examples/.env` (defaults in [`.env.defaults`](.env.defaults)) to print formatted footers:

| Env var | Default | When `true` |
|---------|---------|-------------|
| `SHOW_LLM_USAGE` | `false` | Prints token usage (`prompt_tokens`, `completion_tokens`, etc.) |
| `SHOW_TELEMETRY` | `false` | Prints run telemetry (`total_llm_calls`, tool counts, retriever searches, memory recalls/stores, etc.) |

```bash
SHOW_LLM_USAGE=true go run ./simple_agent "Hello, what can you do?"
SHOW_TELEMETRY=true go run ./simple_agent "Hello, what can you do?"
SHOW_LLM_USAGE=true SHOW_TELEMETRY=true go run ./agent_with_stream "What's 17 * 23?"
```

For retriever examples, `SHOW_TELEMETRY=true` also prints prefetch/agentic search breakdowns — see [agent_with_retriever/README.md](agent_with_retriever/README.md).

For memory examples, `SHOW_TELEMETRY=true` also prints `total_memory_recalls` and `total_memory_stores` — see [agent_with_memory/README.md](agent_with_memory/README.md).

## Env vars

| Env var | Description |
|---------|-------------|
| `AGENT_RUNTIME` | `local` (default), `temporal`, or `restate` — selects the execution backend |
| `TEMPORAL_HOST`, `TEMPORAL_PORT`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASKQUEUE` | Temporal connection (used when `AGENT_RUNTIME=temporal`) |
| `RESTATE_INGRESS_URL`, `RESTATE_ADMIN_URL`, `RESTATE_ENDPOINT_LISTEN_ADDRESS`, `RESTATE_DEPLOYMENT_URL`, `RESTATE_AUTH_KEY` | Restate connection (used when `AGENT_RUNTIME=restate`) — see [restate-setup.md](../restate-setup.md) |
| `REDIS_ADDR` | Redis address for `agent_with_conversation` (default: `localhost:6379`) |
| `CONVERSATION_ID` | Optional session id override for conversation examples |
| `LLM_PROVIDER` | `openai`, `anthropic`, or `gemini` (see `.env.defaults`) |
| `LLM_APIKEY` | API key |
| `LLM_MODEL` | e.g. `gpt-4o`, `claude-3-5-sonnet-20241022` |
| `LLM_BASEURL` | Optional (custom/proxy endpoints) |
| `LOG_ENABLE` | `false` (default) or `true` — when `false`, SDK uses `NoopLogger` (no stderr logs) |
| `LOG_LEVEL` | `error` (default), `warn`, `info`, `debug` — applied when `LOG_ENABLE=true`; logs go to stderr |
| `SHOW_LLM_USAGE` | Set to `true` to print token usage footer after each run (default: `false`) |
| `SHOW_TELEMETRY` | Set to `true` to print run telemetry footer after each run (default: `false`) |
| `SERPER_API_KEY` | For search tool |
| `MCP_TRANSPORT` | **Required** for MCP examples: `stdio` or `streamable_http` (aliases: `local`, `http`, `remote`, …) |
| `MCP_SERVER_NAME` | Optional server id for wiring (defaults: `local` for stdio, `remote` for HTTP) |
| `MCP_STREAMABLE_HTTP_URL` | Remote MCP base URL (required for `streamable_http`) |
| `MCP_STDIO_COMMAND` | Executable for local subprocess MCP (required for `stdio`) |
| `MCP_STDIO_ARGS` | Optional JSON array of argv strings, e.g. `["-y","@scope/pkg","/dir"]` |
| `MCP_STDIO_ENV` | Optional JSON object of extra subprocess env vars |
| `MCP_BEARER_TOKEN` | Optional static bearer for MCP HTTP; ignored when OAuth env trio is all set |
| `MCP_TIMEOUT_SECONDS` | Optional; positive seconds cap MCP connect+RPC timeout |
| `MCP_RETRY_ATTEMPTS` | Optional; max attempts per MCP operation when > 0 |
| `MCP_ALLOW_TOOLS`, `MCP_BLOCK_TOOLS` | Optional comma-separated allow/block tool lists (mutually exclusive) |
| `MCP_CLIENT_ID`, `MCP_CLIENT_SECRET`, `MCP_TOKEN_URL` | Optional together: OAuth2 client credentials for MCP HTTP transport |
| `MCP_SKIP_TLS_VERIFY` | Optional; set to `true` to skip TLS verify for MCP/token HTTP (dev only) |
| `A2A_URL` | **Required** for A2A examples: remote agent base URL |
| `A2A_SERVER_NAME` | Optional connection id (default: `remote`) — used in tool names |
| `A2A_TIMEOUT_SECONDS` | Optional; positive seconds cap per A2A HTTP operation |
| `A2A_TOKEN` | Optional static bearer for the A2A HTTP client |
| `A2A_HEADERS` | Optional JSON object of extra HTTP headers |
| `A2A_SKIP_TLS_VERIFY` | Optional; `true` skips TLS verification for A2A HTTP (dev only) |
| `A2A_ALLOW_SKILLS`, `A2A_BLOCK_SKILLS` | Optional comma-separated allow/block skill ID lists (mutually exclusive) |
| `A2A_SERVER_HOST` | Optional bind hostname for **`agent_with_a2a_server`** (empty → default **localhost**) |
| `A2A_SERVER_PORT` | Optional TCP port for **`agent_with_a2a_server`** (0 → default **9999**) |
| `A2A_SERVER_BEARER_TOKENS` | Optional comma-separated bearer secrets for inbound JSON-RPC on **`agent_with_a2a_server`** |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | **Required** for **`agent_with_observability`**: OTLP collector **`host:port`** (no `http://` scheme), e.g. **`localhost:4317`** (gRPC) or **`localhost:4318`** (HTTP) |
| `OTLP_PROTOCOL` | Optional: **`grpc`** (default) or **`http`** — must match how the collector listens |
| `OTLP_INSECURE` | Optional: **`true`** for plaintext export (typical for local collectors without TLS) |
| `RETRIEVER_MODE` | For **`agent_with_retriever`**: **`agentic`** (default), **`prefetch`**, or **`hybrid`** |
| `WEAVIATE_HOST`, `WEAVIATE_SCHEME`, `WEAVIATE_CLASS`, … | Weaviate backend — **`.env.defaults`** and **[agent_with_retriever/README.md#weaviate](agent_with_retriever/README.md#weaviate)** |
| `PGVECTOR_DSN`, `PGVECTOR_TABLE`, `EMBEDDING_OPENAI_MODEL`, … | pgvector backend — **`PGVECTOR_DSN` required**; **[agent_with_retriever/README.md#pgvector](agent_with_retriever/README.md#pgvector)** |
| `MEMORY_USER_ID`, `MEMORY_STORE_MODE`, `MEMORY_RECALL_ENABLED`, `MEMORY_RECALL_LIMIT`, `MEMORY_RECALL_MIN_SCORE` | For **`agent_with_memory`**: scope user, store mode (`always` / `ondemand`), recall settings — **[agent_with_memory/README.md](agent_with_memory/README.md)** |
| `WEAVIATE_MEMORY_CLASS`, `PGVECTOR_MEMORY_TABLE` | Memory backend class/table names (defaults: `AgentMemory`, `agent_memories`) |
