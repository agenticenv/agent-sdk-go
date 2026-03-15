# CLI

Interactive conversation mode. Type prompts, get responses. Type `exit`, `quit`, or `bye` to end.

## Prerequisites

**Temporal server** must be running. Start a local dev server with Docker:

```bash
docker run --rm -p 7233:7233 -p 8233:8233 temporalio/temporal:latest server start-dev --ip 0.0.0.0
```

- **Temporal service:** localhost:7233
- **Web UI:** http://localhost:8233

Or use [Temporal CLI](https://docs.temporal.io/cli/setup-cli): `temporal server start-dev`.

For production or self-hosted (Docker Compose, Kubernetes): [Temporal Cloud](https://docs.temporal.io/production-deployment) | [Self-hosted deployment](https://docs.temporal.io/self-hosted-guide/deployment)

The CLI uses `temporal.host`, `temporal.port`, `temporal.namespace` from `config.yaml` (default: localhost, 7233, default). Override with `CMD_TEMPORAL_HOST`, `CMD_TEMPORAL_PORT`, `CMD_TEMPORAL_NAMESPACE` if Temporal runs elsewhere.

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
go build -o cmd/bin/agent ./cmd
./cmd/bin/agent
```

The `cmd/bin/` directory is gitignored.

## Config

Config is loaded from `cmd/config.yaml` (default when run from project root). Env vars with `CMD_` prefix override file values.

| Env var | Description |
|---------|-------------|
| `CMD_TEMPORAL_HOST`, `CMD_TEMPORAL_PORT`, `CMD_TEMPORAL_NAMESPACE`, `CMD_TEMPORAL_TASKQUEUE` | Temporal connection |
| `CMD_LLM_PROVIDER` | `openai` or `anthropic` |
| `CMD_LLM_APIKEY` | **Required.** API key (not in config file for security) |
| `CMD_LLM_MODEL` | e.g. `gpt-4o` |
| `CMD_LLM_BASEURL` | Optional |
| `CMD_LOGGER_LEVEL` | `error` (default), `warn`, `info`, `debug` |
| `CMD_LOGGER_OUTPUT` | Log file path; default `logs/agent.log` |

Example:

```bash
export CMD_LLM_APIKEY=sk-your-key
go run ./cmd
```

## Logging

The CLI shows only **user prompts and agent responses** on the console. Internal logs go to a file.

- **Default log file:** `cmd/logs/agent.log` (resolved from project root; gitignored)
- **Configure:** Set `logger.output` in `config.yaml` or `CMD_LOGGER_OUTPUT`
- **Directories:** `logs/` and `cmd/bin/` are gitignored
