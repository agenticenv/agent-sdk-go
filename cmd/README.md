# CLI

The `agentctl` CLI uses **agent-sdk-go** and therefore needs a **running Temporal server** — same as the library examples.

Interactive conversation mode. Type prompts, get responses. Type `exit`, `quit`, or `bye` to end.

## Configuration

1. **Copy the sample config** and add your values:

   ```bash
   cp cmd/config.sample.yaml cmd/config.yaml
   ```

2. **Edit `cmd/config.yaml`** with your Temporal host, LLM provider, API key, and model. Optional **MCP** servers live under `mcp.servers` in `config.sample.yaml`: set `enabled: true` on entries you want (stdio subprocess or `streamable_http` URL); leave others `enabled: false`.

3. **Optional:** Use environment variables to override (keeps secrets out of the config file):

   ```bash
   export AGENT_LLM_APIKEY=sk-your-key
   export AGENT_LLM_PROVIDER=openai
   export AGENT_LLM_MODEL=gpt-4o
   go run ./cmd
   ```

- **config.sample.yaml** — template (committed to repo)
- **config.yaml** — your config (gitignored; do not commit)

## Prerequisites

**Temporal server** must be running. Start a local dev server with Docker:

```bash
docker run --rm -p 7233:7233 -p 8233:8233 temporalio/temporal:latest server start-dev --ip 0.0.0.0
```

- **Temporal service:** localhost:7233
- **Web UI:** http://localhost:8233

Or use [Temporal CLI](https://docs.temporal.io/cli/setup-cli): `temporal server start-dev`.

For production or self-hosted (Docker Compose, Kubernetes): [Temporal Cloud](https://docs.temporal.io/production-deployment) | [Self-hosted deployment](https://docs.temporal.io/self-hosted-guide/deployment)

The CLI uses `temporal.host`, `temporal.port`, `temporal.namespace` from `config.yaml` (default: localhost, 7233, default). Override with `AGENT_TEMPORAL_HOST`, `AGENT_TEMPORAL_PORT`, `AGENT_TEMPORAL_NAMESPACE` if Temporal runs elsewhere.

## Run

```bash
go run ./cmd
```

Or with a custom config path:

```bash
go run ./cmd -config cmd/config.yaml
```

## Build

From project root:

```bash
make build
./cmd/bin/agentctl
```

Or build manually with an embedded version (matches `make build`, which uses `git describe`):

```bash
go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o cmd/bin/agentctl ./cmd
```

A plain `go build ./cmd` leaves the version as `dev` until you pass `-ldflags`. **Release binaries** from GitHub get the tag via GoReleaser (see `.goreleaser.yaml`, `-X main.version=<git tag>`).

The `cmd/bin/` directory is gitignored.

## Install

Install `agentctl` to `$(go env GOPATH)/bin` so you can run it from anywhere (ensure that directory is in your PATH):

```bash
make install
agentctl -config cmd/config.yaml
```

## Config file and env vars

Config is loaded from `cmd/config.yaml` (default). If the file does not exist, defaults plus env vars are used.

| Env var | Description |
|---------|-------------|
| `AGENT_TEMPORAL_HOST`, `AGENT_TEMPORAL_PORT`, `AGENT_TEMPORAL_NAMESPACE`, `AGENT_TEMPORAL_TASKQUEUE` | Temporal connection |
| `AGENT_LLM_PROVIDER` | `openai` \| `anthropic` \| `gemini` |
| `AGENT_LLM_APIKEY` | LLM API key (preferred over putting in config file) |
| `AGENT_LLM_MODEL` | e.g. `gpt-4o`, `claude-haiku-4-5`, `gemini-2.5-flash` |
| `AGENT_LLM_BASEURL` | Optional; for OpenAI-compatible proxies |
| `AGENT_LOGGER_LEVEL` | `error` (default), `warn`, `info`, `debug` |
| `AGENT_LOGGER_OUTPUT` | Log file path; default `cmd/logs/agent.log` |

### MCP (optional)

Define `mcp.servers` in `config.yaml` (see `config.sample.yaml`). Each entry supports **`enabled`** (omit or `true` to use; `false` to skip), **`name`** (stable id for that MCP connection), **`transport`** (`stdio` or `streamable_http`), plus transport-specific fields (`command` / `args` / `env` for stdio; `url` / `bearer_token` / OAuth / `headers` for HTTP). Optional **`timeout_seconds`**, **`retry_attempts`**, **`allow_tools`** / **`block_tools`**.

When at least one server is enabled, the CLI registers **`WithMCPConfig`** and **`AutoToolApprovalPolicy`** so MCP tools run without per-call approval in the REPL.

## Logging

The CLI shows only **user prompts and agent responses** on the console. Internal logs go to a file.

- **Default log file:** `cmd/logs/agent.log` (resolved from project root; gitignored)
- **Configure:** Set `logger.output` in `config.yaml` or `AGENT_LOGGER_OUTPUT`
- **Directories:** `logs/` and `cmd/bin/` are gitignored
