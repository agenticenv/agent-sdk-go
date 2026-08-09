# A2A client examples (`agent_with_a2a_config`, `agent_with_a2a_client`)

Outbound A2A: your agent calls **remote** A2A agents as tools.

- **`agent_with_a2a_config`** — `agent.WithA2AConfig(agent.A2AServers{<serverName>: cfg})`; SDK builds the default **[pkg/a2a/client](https://pkg.go.dev/github.com/agenticenv/agent-sdk-go/pkg/a2a/client)** per server.
- **`agent_with_a2a_client`** — `a2aclient.NewClient(...)` + **`WithA2AClients`**; same env and testing flow as above.

## Prerequisites

- **LLM** in **`examples/.env`** (see **`../.env.defaults`**). Durable runtime only when **`AGENT_RUNTIME=temporal`** or **`AGENT_RUNTIME=restate`**.

**Required for A2A:** **`A2A_URL`** — base URL of the remote agent (scheme + host + port, **no path**). Optional: **`A2A_SERVER_NAME`**, **`A2A_TOKEN`**, **`A2A_HEADERS`** (JSON), **`A2A_TIMEOUT_SECONDS`**, **`A2A_SKIP_TLS_VERIFY`** (dev), **`A2A_ALLOW_SKILLS`** / **`A2A_BLOCK_SKILLS`** (comma-separated; mutually exclusive).

## Run

From **`examples/`**:

```bash
go run ./agent_with_a2a_config
go run ./agent_with_a2a_config "What tools do you have available?"

go run ./agent_with_a2a_client
go run ./agent_with_a2a_client "What tools do you have available?"
```

## Testing against a real A2A server

There is no fixed demo URL in this repo—run an A2A-compatible HTTP server locally (or use your deployment), set **`A2A_URL`** to its **base URL**. The SDK loads the agent card from the well-known path (same as [a2aproject/a2a-go](https://github.com/a2aproject/a2a-go) `a2asrv.WellKnownAgentCardPath`) and uses the JSON-RPC endpoint advertised on the card.

### Worked example — Python helloworld (`a2a-samples`)

```bash
git clone https://github.com/a2aproject/a2a-samples
cd a2a-samples/samples/python/agents/helloworld
uv run .   # server on port 9999 by default
```

In another terminal:

```bash
curl -sS "http://localhost:9999/.well-known/agent-card.json" | head
```

In **`examples/.env`** set **`A2A_URL=http://localhost:9999`** (or **`http://127.0.0.1:9999`**), then from **`examples/`**:

```bash
go run ./agent_with_a2a_config
```

### Other sample servers

| Source | Notes |
|--------|--------|
| **[a2aproject/a2a-samples](https://github.com/a2aproject/a2a-samples)** | Official samples; helloworld uses **9999** by default — align **`A2A_URL`**. |
| **[a2aproject/a2a-go](https://github.com/a2aproject/a2a-go)** | Same **`a2asrv`** stack as **`pkg/a2a/client`** tests; run their HTTP examples and set **`A2A_URL`** to the base URL (card + JSON-RPC). |
| **Your own agent** | Any deployment with a valid agent card and protocol endpoint. |

### Quick sanity check

Replace host/port if needed:

```bash
curl -sS "http://localhost:9999/.well-known/agent-card.json" | head
```

## Env vars (A2A client)

See **`A2A_*`** rows in **[examples/README.md](../README.md#env-vars)** and **`../.env.defaults`**.
