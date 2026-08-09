# MCP examples (`agent_with_mcp_config`, `agent_with_mcp_client`)

These two programs use the **same env-driven MCP transport** but wire the agent differently:

- **`agent_with_mcp_config`** — `agent.WithMCPConfig(agent.MCPServers{<serverName>: mcpCfg})`. The SDK builds the default MCP client per server.
- **`agent_with_mcp_client`** — `mcpclient.NewClient(<serverName>, transport, opts...)` then `agent.WithMCPClients(client)`.

## Prerequisites

- **`examples/.env`** with **`LLM_*`** set (see **`../.env.defaults`**; defaults load automatically).
- Durable runtime (optional): **`AGENT_RUNTIME=temporal`** per **[temporal-setup.md](../../temporal-setup.md)** / `task infra:temporal:up`, or **`AGENT_RUNTIME=restate`** per **[restate-setup.md](../../restate-setup.md)** / `task infra:restate:up`.

## Configure MCP

**Transport** must be set with **`MCP_TRANSPORT`**: `stdio` or `streamable_http` (aliases in **`.env.defaults`**).

- **Remote — `streamable_http`:** set **`MCP_STREAMABLE_HTTP_URL`**. Auth optional: **`MCP_BEARER_TOKEN`**, or OAuth trio **`MCP_CLIENT_ID`** + **`MCP_CLIENT_SECRET`** + **`MCP_TOKEN_URL`** (OAuth wins over bearer when all three are set). **`MCP_SKIP_TLS_VERIFY=true`** for dev TLS only.
- **Local — `stdio`:** set **`MCP_STDIO_COMMAND`** and optional **`MCP_STDIO_ARGS`** (JSON string array) and **`MCP_STDIO_ENV`** (JSON string→string object).

Shared optional knobs: **`MCP_SERVER_NAME`**, **`MCP_TIMEOUT_SECONDS`**, **`MCP_RETRY_ATTEMPTS`**, **`MCP_ALLOW_TOOLS`** / **`MCP_BLOCK_TOOLS`** (comma-separated; only one list type).

See **`../.env.defaults`** for every variable.

## Run

From the **`examples/`** directory:

```bash
go run ./agent_with_mcp_config
go run ./agent_with_mcp_config "List tools you can call."

go run ./agent_with_mcp_client
go run ./agent_with_mcp_client "List tools you can call."
```

## Testing against real MCP servers

This repo does **not** start an MCP server—you point **`examples/.env`** at **your** server(s). Pick **stdio** or **streamable_http**, set **`MCP_TRANSPORT`**, then fill in **`.env.defaults`** under **MCP**.

### Worked example — TypeScript streamable HTTP (`mcp-streamable-http`)

```bash
git clone https://github.com/invariantlabs-ai/mcp-streamable-http
cd mcp-streamable-http/typescript-example/server
npm install && npm run build
node build/index.js
```

The server listens on **port 8123** by default with MCP at **`/mcp`** (override with **`node build/index.js --port=XXXX`**). In **`examples/.env`** set:

- **`MCP_TRANSPORT=streamable_http`**
- **`MCP_STREAMABLE_HTTP_URL=http://localhost:8123/mcp`**

Then from **`examples/`**:

```bash
go run ./agent_with_mcp_config
```

Quick check (while the server is running):

```bash
curl -sS -o /dev/null -w "%{http_code}\n" "http://localhost:8123/mcp"
```

### Modes

| Mode | What you need |
|------|----------------|
| **`stdio`** | A runnable MCP server binary or script (often **Node**, **Python**, or **Go**) launched as a subprocess. Set **`MCP_STDIO_COMMAND`** and, if needed, **`MCP_STDIO_ARGS`** (JSON array) and **`MCP_STDIO_ENV`**. The SDK spawns the process and speaks MCP over stdin/stdout. |
| **`streamable_http`** | An MCP server listening with the **streamable HTTP** transport. Set **`MCP_STREAMABLE_HTTP_URL`** to the MCP endpoint URL (often includes a path such as **`/mcp`**). Optional **`MCP_BEARER_TOKEN`** or OAuth (**`MCP_CLIENT_ID`** / **`MCP_CLIENT_SECRET`** / **`MCP_TOKEN_URL`**). Use **`MCP_SKIP_TLS_VERIFY=true`** only for local/dev HTTPS. |

### Where to find servers and docs

| Source | Notes |
|--------|--------|
| **[invariantlabs-ai/mcp-streamable-http](https://github.com/invariantlabs-ai/mcp-streamable-http)** | Reference **streamable HTTP** server (TypeScript example above). Default **8123**, path **`/mcp`**; set **`MCP_STREAMABLE_HTTP_URL`** to the full endpoint URL. |
| **[modelcontextprotocol/servers](https://github.com/modelcontextprotocol/servers)** | Reference implementations (filesystem, git, fetch, etc.). Often **npx** / **uvx** / **docker** per each server’s README; map into **`MCP_STDIO_COMMAND`** + **`MCP_STDIO_ARGS`**. |
| **[Model Context Protocol](https://modelcontextprotocol.io)** | Protocol docs; third-party hosts list streamable-HTTP endpoints you can point **`MCP_STREAMABLE_HTTP_URL`** at. |
| **Your own MCP server** | Any compliant implementation—the examples need **`stdio`** or **`streamable_http`** as wired in **`.env.defaults`**. |

### Quick checks before running

- **`streamable_http`:** Confirm the URL is reachable from the machine running the example. Example: `curl -sS -o /dev/null -w "%{http_code}\n" "$MCP_STREAMABLE_HTTP_URL"` — status depends on the implementation.
- **`stdio`:** Run the same command line as **`MCP_STDIO_COMMAND`** / **`MCP_STDIO_ARGS`** in a terminal once to ensure the binary starts.

You still need **LLM** credentials in **`examples/.env`** (and Temporal/Restate when using the matching **`AGENT_RUNTIME`**).

## Env vars (MCP)

See the **MCP_*** rows in **[examples/README.md](../README.md#env-vars)** and **`../.env.defaults`**.
