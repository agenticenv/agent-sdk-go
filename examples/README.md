# Examples

These programs exercise **agent-sdk-go** (`github.com/agenticenv/agent-sdk-go`). The SDK is **Temporal-only**: there is no way to run these examples without a Temporal cluster—agents execute as Temporal workflows, so a running Temporal service is mandatory for every example below.

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
| `simple_agent` | Minimal agent, no tools — Temporal config, system prompt, LLM client, single `Run()`; prints `AgentResponse.Usage` (token counts) when the provider reports them |
| `agent_with_temporal_client` | Caller-owned Temporal client — `WithTemporalClient` + `WithTaskQueue`; create and close client yourself (TLS, API key, Cloud) |
| `agent_with_conversation` | In-memory conversation with `WithConversation` — multi-turn context, same `conversationID` for `Run` |
| `agent_with_tools` | Built-in tools (echo, calculator, weather, wikipedia, search) with auto-approval |
| `agent_with_stream` | Streaming with `Stream` + partial content (`content_delta`, `tool_call`, `complete`); prints aggregated token usage on `complete` |
| `agent_with_stream_conversation` | Stream + conversation; event handling to avoid duplicate output (ContentDelta vs Complete) |
| `agent_with_tools_approval` | Tools + `WithApprovalHandler` — user approves or rejects each tool run (Run only) |
| `agent_with_run_async` | `RunAsync` — `resultCh` + `approvalCh`; use `req.Respond` (no `WithApprovalHandler`) |
| `agent_with_custom_tools` | Custom tools via `WithTools` — implementing `interfaces.Tool` |
| `multiple_agents` | Multiple agents with `WithInstanceId` — sequential or concurrent |
| `agent_with_subagents` | Main agent + math specialist — `WithSubAgents`, separate task queues |
| `agent_with_json_response` | Structured LLM output — `WithResponseFormat` + `interfaces.JSONSchema` (JSON with schema; no tools) |
| `agent_with_reasoning` | Generic `interfaces.LLMReasoning` via `WithLLMSampling` — `Stream` to observe `thinking_delta` (e.g. Anthropic) |
| `agent_with_worker` | Agent and worker in separate processes — `DisableLocalWorker` + `NewAgentWorker` |
| `agent_with_mcp_config` | MCP via `WithMCPConfig` — transport from env: `mcp.MCPStdio` (command, JSON args/env) or `mcp.MCPStreamableHTTP` (URL, optional bearer/OAuth); see `examples/env.sample` |
| `agent_with_mcp_client` | Same transports via `mcpclient.NewClient` + `WithMCPClients`; same env vars as `agent_with_mcp_config` |

## Setup

```bash
cp env.sample .env
# Edit .env: set LLM_APIKEY, LLM_MODEL (see LLM_PROVIDER: openai, anthropic, or gemini)
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

### Structured JSON response (`WithResponseFormat`)

Uses `agent.WithResponseFormat` with `interfaces.ResponseFormatJSON`, `Name`, and `interfaces.JSONSchema`. No tools—keeps the run in structured-output mode. Prints validated, indented JSON.

```bash
go run ./agent_with_json_response
go run ./agent_with_json_response "What is the capital of Japan?"
```

### Reasoning / thinking (`WithLLMSampling` + `LLMReasoning`)

Sets `WithLLMSampling` with `Reasoning: &interfaces.LLMReasoning{Enabled, Effort, BudgetTokens}` and uses **`Stream`** so you can see **`thinking_delta`** events when the provider emits them (e.g. Anthropic extended thinking). Pick a model that supports reasoning/thinking for your `LLM_PROVIDER`.

```bash
go run ./agent_with_reasoning
go run ./agent_with_reasoning "Why is the sky blue? One short paragraph."
```

### Streaming + conversation (event handling pattern)

Interactive multi-turn with `Stream`. Demonstrates how to handle ContentDelta/Content and Complete to avoid printing the same text twice.

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

### MCP (`WithMCPConfig` vs `WithMCPClients`)

Two examples use the **same env-driven transport** but wire the agent differently:

- **`agent_with_mcp_config`** — `agent.WithMCPConfig(agent.MCPServers{<serverName>: mcpCfg})`. The SDK builds the default MCP client per server.
- **`agent_with_mcp_client`** — `mcpclient.NewClient(<serverName>, transport, opts...)` then `agent.WithMCPClients(client)`.

**Transport** must be set explicitly with **`MCP_TRANSPORT`**: `stdio` or `streamable_http` (see aliases in **`env.sample`**). See **`env.sample`** for every variable.

- **Remote — `streamable_http`:** set **`MCP_STREAMABLE_HTTP_URL`**. Auth optional: **`MCP_BEARER_TOKEN`**, or OAuth trio **`MCP_CLIENT_ID`** + **`MCP_CLIENT_SECRET`** + **`MCP_TOKEN_URL`** (OAuth wins over bearer when all three are set). **`MCP_SKIP_TLS_VERIFY=true`** for dev TLS only.
- **Local — `stdio`:** set **`MCP_STDIO_COMMAND`** and optional **`MCP_STDIO_ARGS`** (JSON string array) and **`MCP_STDIO_ENV`** (JSON string→string object).

Shared optional knobs: **`MCP_SERVER_NAME`**, **`MCP_TIMEOUT_SECONDS`**, **`MCP_RETRY_ATTEMPTS`**, **`MCP_ALLOW_TOOLS`** / **`MCP_BLOCK_TOOLS`** (comma-separated; only one list type).

```bash
go run ./agent_with_mcp_config
go run ./agent_with_mcp_config "List tools you can call."

go run ./agent_with_mcp_client
go run ./agent_with_mcp_client "List tools you can call."
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
| `LLM_PROVIDER` | `openai`, `anthropic`, or `gemini` (see `env.sample`) |
| `LLM_APIKEY` | API key |
| `LLM_MODEL` | e.g. `gpt-4o`, `claude-3-5-sonnet-20241022` |
| `LLM_BASEURL` | Optional (custom/proxy endpoints) |
| `LOG_LEVEL` | `error` (default), `warn`, `info`, `debug` — logs go to stderr |
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
