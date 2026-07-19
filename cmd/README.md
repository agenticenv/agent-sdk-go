# CLI

`agctl` is the interactive CLI for **agent-sdk-go**. It is its **own Go module** (`cmd/go.mod`) so the SDK library stays free of CLI-only dependencies. Its `go.mod` uses `replace github.com/agenticenv/agent-sdk-go => ../` to build against the SDK in this repo, so **run every command from the `cmd/` directory** (or use the installed binary).

Interactive conversation mode. Type prompts, get responses. Type `exit`, `quit`, or `bye` to end.

## Configuration

1. **Copy the sample config** and add your values (from `cmd/`):

   ```bash
   cp config.sample.yaml config.yaml
   ```

2. **Edit `config.yaml`** with your Temporal host, LLM provider, API key, and model. Optional **MCP** servers live under `mcp.servers` in `config.sample.yaml`: set `enabled: true` on entries you want (stdio subprocess or `streamable_http` URL); leave others `enabled: false`.

3. **Optional:** Use environment variables to override (keeps secrets out of the config file):

   ```bash
   export AGENT_LLM_APIKEY=sk-your-key
   export AGENT_LLM_PROVIDER=openai
   export AGENT_LLM_MODEL=gpt-4o
   go run .
   ```

- **config.sample.yaml** — template (committed to repo)
- **config.yaml** — your config (gitignored; do not commit)

The CLI uses `temporal.host`, `temporal.port`, and `temporal.namespace` from `config.yaml` (default: localhost, 7233, default). Override with `AGENT_TEMPORAL_HOST`, `AGENT_TEMPORAL_PORT`, and `AGENT_TEMPORAL_NAMESPACE` if Temporal runs elsewhere.

**Anthropic prompt caching:** when `llm.provider: anthropic`, the CLI always enables `llm.WithPromptCaching(true)` — this differs from the SDK library default, which is caching **off** unless you opt in explicitly (see [LLM Providers](../docs/getting-started/llm-providers.mdx#prompt-caching)). Set `show_llm_usage: true` to see `cached_prompt` in the exit summary and confirm cache hits.

## Run

From the `cmd/` directory:

```bash
task run
# or
go run .
```

Or with a custom config path:

```bash
go run . -config /path/to/config.yaml
```

## Build

Uses [Task](https://taskfile.dev) (`Taskfile.yml` in this directory). From `cmd/`:

```bash
task build
./bin/agctl
```

`task build` embeds the version via `git describe` (`-ldflags "-X main.version=..."`). A plain `go build .` leaves the version as `dev`. **Release binaries** from GitHub get the tag via GoReleaser (see `../.goreleaser.yaml`, which builds this module with `dir: ./cmd`).

The `cmd/bin/` directory is gitignored.

## Install

Install `agctl` to `$(go env GOPATH)/bin` so you can run it from anywhere (ensure that directory is in your PATH). From `cmd/`:

```bash
task install
agctl -config config.yaml
```

## Config file and env vars

Config is loaded from `config.yaml` in the current directory (default; run from `cmd/`). Override with `-config <path>`. If the file does not exist, defaults plus env vars are used.

| Env var | Description |
|---------|-------------|
| `AGENT_TEMPORAL_HOST`, `AGENT_TEMPORAL_PORT`, `AGENT_TEMPORAL_NAMESPACE`, `AGENT_TEMPORAL_TASKQUEUE` | Temporal connection |
| `AGENT_LLM_PROVIDER` | `openai` \| `anthropic` \| `gemini` |
| `AGENT_LLM_APIKEY` | LLM API key (preferred over putting in config file) |
| `AGENT_LLM_MODEL` | e.g. `gpt-4o`, `claude-haiku-4-5`, `gemini-2.5-flash` |
| `AGENT_LLM_BASEURL` | Optional; for OpenAI-compatible proxies |
| `AGENT_LOGGER_LEVEL` | `error` (default), `warn`, `info`, `debug` |
| `AGENT_LOGGER_OUTPUT` | Log file path; default `cmd/logs/agctl.log` |
| `AGENT_SHOW_LLM_USAGE` | When `true`, print accumulated session token usage on exit (`show_llm_usage` in config) |

### MCP (optional)

Define `mcp.servers` in `config.yaml` (see `config.sample.yaml`). Each entry supports **`enabled`** (omit or `true` to use; `false` to skip), **`name`** (stable id for that MCP connection), **`transport`** (`stdio` or `streamable_http`), plus transport-specific fields (`command` / `args` / `env` for stdio; `url` / `bearer_token` / OAuth / `headers` for HTTP). Optional **`timeout_seconds`**, **`retry_attempts`**, **`allow_tools`** / **`block_tools`**.

When at least one server is enabled, the CLI registers **`WithMCPConfig`** and **`AutoToolApprovalPolicy`** so MCP tools run without per-call approval in the REPL.

## Logging

The CLI shows only **user prompts and agent responses** on the console. Internal logs go to a file.

- **Default log file:** `cmd/logs/agctl.log` (resolved from the module root; gitignored)
- **Configure:** Set `logger.output` in `config.yaml` or `AGENT_LOGGER_OUTPUT`
- **Directories:** `logs/` and `cmd/bin/` are gitignored
