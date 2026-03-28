# Examples

These programs exercise **agent-sdk-go** (`github.com/vvsynapse/agent-sdk-go`). The SDK **requires a Temporal cluster**: agents execute as Temporal workflows, so a running Temporal service is mandatory—not optional—for every example below.

## Prerequisites

**Temporal server** must be running. Start a local dev server with Docker:

```bash
docker run --rm -p 7233:7233 -p 8233:8233 temporalio/temporal:latest server start-dev --ip 0.0.0.0
```

- **Temporal service:** localhost:7233
- **Web UI:** http://localhost:8233

Or use [Temporal CLI](https://docs.temporal.io/cli/setup-cli): `temporal server start-dev`.

For production or self-hosted (Docker Compose, Kubernetes): [Temporal Cloud](https://docs.temporal.io/production-deployment) | [Self-hosted deployment](https://docs.temporal.io/self-hosted-guide/deployment)

The examples use `TEMPORAL_HOST`, `TEMPORAL_PORT`, `TEMPORAL_NAMESPACE` from `.env` (default: localhost, 7233, default). Adjust if your Temporal runs elsewhere.

## Examples overview

| Example | What it demonstrates |
|---------|---------------------|
| `simple_agent` | Minimal agent, no tools — Temporal config, system prompt, LLM client, single `Run()` |
| `agent_with_temporal_client` | Caller-owned Temporal client — `WithTemporalClient` + `WithTaskQueue`; create and close client yourself (TLS, API key, Cloud) |
| `agent_with_conversation` | In-memory conversation with `WithConversation` — multi-turn context, same `conversationID` for `Run` |
| `agent_with_tools` | Built-in tools (echo, calculator, weather, wikipedia, search) with auto-approval |
| `agent_with_stream` | Streaming with `RunStream` + partial content (`content_delta`, `tool_call`, `complete`) |
| `agent_with_stream_conversation` | RunStream + conversation; event handling to avoid duplicate output (ContentDelta vs Complete) |
| `agent_with_tools_approval` | Tools + `WithApprovalHandler` — user approves or rejects each tool run (Run only) |
| `agent_with_run_async` | `RunAsync` — `resultCh` + `approvalCh`; use `req.Respond` (no `WithApprovalHandler`) |
| `agent_with_custom_tools` | Custom tools via `WithTools` — implementing `interfaces.Tool` |
| `multiple_agents` | Multiple agents with `WithInstanceId` — sequential or concurrent |
| `agent_with_subagents` | Main agent + math specialist — `WithSubAgents`, separate task queues |
| `agent_with_worker` | Agent and worker in separate processes — `DisableWorker` + `NewAgentWorker` |

## Setup

```bash
cp env.sample .env
# Edit .env: set LLM_APIKEY, LLM_MODEL (gpt-4o or claude-3-5-sonnet-20241022)
```

## Run examples

### Minimal agent (no tools)

```bash
go run ./simple_agent "Hello, what can you do?"
```

### Agent with caller-owned Temporal client

Uses `WithTemporalClient` and `WithTaskQueue`. The example creates the Temporal client, passes it to the agent, and closes it when done. Use this pattern for TLS, Temporal Cloud API keys, or other connection options.

```bash
go run ./agent_with_temporal_client "Hello, what can you do?"
```

### Agent with conversation (multi-turn)

Uses in-memory conversation. Run **interactive mode** (no args) for multi-turn in one process—history is shared across turns. With args, runs a single turn (useful for testing).

```bash
# Interactive: type prompts, get responses; history shared. Type 'exit' to end.
go run ./agent_with_conversation

# Single turn (new process each run; no shared history)
go run ./agent_with_conversation "Hello, remember I'm Alice"
```

### Agent with tools

```bash
go run ./agent_with_tools "What's the weather in Tokyo?"
```

### Streaming (partial content as tokens arrive)

```bash
go run ./agent_with_stream "What's the current time and what's 17 * 23?"
```

### Streaming + conversation (event handling pattern)

Interactive multi-turn with `RunStream`. Demonstrates how to handle ContentDelta/Content and Complete to avoid printing the same text twice.

```bash
go run ./agent_with_stream_conversation
go run ./agent_with_stream_conversation "What is 5 * 8?"
```

### Sub-agents (main agent + specialist)

Two agents in one process: main agent with a math specialist registered via `WithSubAgents`. Requires workers on **both** task queues (each `NewAgent` starts its own embedded worker). Main agent uses default tool approval (**RequireAll**): delegating to the specialist prompts on **stdin** (`y` / `n`). Specialist uses **AutoToolApprovalPolicy** so calculator does not prompt. Same stdin pattern as [agent_with_tools_approval](#tools--approval-custom-tools-multiple-agents-worker-split).

```bash
go run ./agent_with_subagents "What is 987 times 654?"
```

### Tools + approval, custom tools, multiple agents, worker split

```bash
go run ./agent_with_tools_approval "What is 15 + 27?"
go run ./agent_with_run_async "What is 15 + 27?"
go run ./agent_with_custom_tools "Reverse 'hello world'"
go run ./multiple_agents "What is 7 times 8?"
go run ./multiple_agents concurrent "What is 7 times 8?"

# Agent and worker in separate processes: start worker first, then agent
go run ./agent_with_worker/worker &   # start worker in background
go run ./agent_with_worker/agent "Hello from remote agent!"
```

## Logging

Examples send conversation (user prompt, assistant response) to **stdout** and internal logs to **stderr**. By default only errors are logged.

- **See logs while evaluating:** Set `LOG_LEVEL=info` or `LOG_LEVEL=debug` in `.env`, or run:
  ```bash
  LOG_LEVEL=debug go run ./simple_agent "Hello, what can you do?"
  ```
- **Save logs to a file:** Redirect stderr to a file:
  ```bash
  LOG_LEVEL=info go run ./simple_agent "Hello" 2>debug.log
  ```
- **Suppress logs:** Show only conversation output:
  ```bash
  go run ./simple_agent "Hello" 2>/dev/null
  ```

## Env vars

| Env var | Description |
|---------|-------------|
| `TEMPORAL_HOST`, `TEMPORAL_PORT`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASKQUEUE` | Temporal connection |
| `LLM_PROVIDER` | `openai` or `anthropic` |
| `LLM_APIKEY` | API key |
| `LLM_MODEL` | e.g. `gpt-4o`, `claude-3-5-sonnet-20241022` |
| `LLM_BASEURL` | Optional (custom/proxy endpoints) |
| `LOG_LEVEL` | `error` (default), `warn`, `info`, `debug` — logs go to stderr |
| `SERPER_API_KEY` | For search tool |
