# `agent_with_a2a_server`

Runs **your** agent as an **inbound** A2A HTTP server (dynamic agent card + **JSON-RPC v2** on **`POST /`** — PascalCase methods such as **`SendMessage`**, **`SendStreamingMessage`**, per **`a2asrv`**).

## Prerequisites

- **LLM** in **`examples/.env`** (see **`../.env.defaults`**). Durable runtime only when **`AGENT_RUNTIME=temporal`** or **`AGENT_RUNTIME=restate`**.
- Optional **`A2A_SERVER_HOST`**, **`A2A_SERVER_PORT`** (defaults **localhost:9999**).
- Optional **`A2A_SERVER_BEARER_TOKENS`** — comma-separated secrets; JSON-RPC calls must send **`Authorization: Bearer <token>`** (agent card GET stays unauthenticated).

## Run

From **`examples/`**:

```bash
go run ./agent_with_a2a_server
```

The process prints the **base URL** and **agent card URL** to stderr; **Ctrl+C** to stop.

## Quick checks

Replace host/port if you changed **`A2A_SERVER_*`**:

```bash
curl -sS "http://localhost:9999/.well-known/agent-card.json" | head
```

## Test as a client from this repo

Second terminal, **`examples/`**:

```bash
export A2A_URL=http://localhost:9999
go run ./agent_with_a2a_config "What tools do you have?"
```

See **[../agent_with_a2a_config/README.md](../agent_with_a2a_config/README.md)** for remote-server testing patterns.

## Test with the `a2a` CLI

Install from [a2aproject/a2a-go — CLI](https://github.com/a2aproject/a2a-go#-cli):

```bash
go install github.com/a2aproject/a2a-go/v2/cmd/a2a@latest

a2a discover http://localhost:9999
a2a send http://localhost:9999 "Hello, what can you do?"
```

Full flags: **`a2a help`**; reference: [**a2a-go** `cmd/README.md`](https://github.com/a2aproject/a2a-go/tree/main/cmd). With **`A2A_SERVER_BEARER_TOKENS`**, see **`a2a help send`** for bearer options.

## Env vars (inbound server)

See **`A2A_SERVER_*`** rows in **[examples/README.md](../README.md#env-vars)** and **`../.env.defaults`**.
