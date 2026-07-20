# agctl

Hacking on the CLI? Work from this directory. It is a separate Go module (`cli/go.mod`) that `replace`s the SDK with `../`, so your changes to both build together.

## Run and build

```bash
cd cli

# Fast iteration
go run . chat
go run . run --prompt "hello"
go run . config show

# Local YAML overrides (config.yaml is gitignored)
go run . --config ./config.yaml chat

# Binary + CI parity
task build          # ./bin/agctl  (version prints "dev")
task test
task check          # lint, test, build — same as the agctl CI job
```

For LLM calls you still need a key — `export AGCTL_LLM_APIKEY=…` or put it in `./config.yaml`. After editing `default.yaml`, rebuild or `go run` so the embedded copy is refreshed.

From the repo root, SDK-wide checks include the CLI: `task cli:check` (or `cd cli && task check`).

## How a command runs

1. **Kong** parses flags/subcommands (`CLI` in `cli.go`).
2. **`config.LoadConfig`** merges layers into one `*config.Config` (see below).
3. The subcommand’s **`Run`** method runs — chat/run call **`ApplyAgentOverrides`** for per-invocation flags.
4. **`internal/agent.Build`** constructs the SDK agent (LLM, tools, MCP, conversation store, runtime).

Most behavior changes belong in `internal/config` or `internal/agent`, not duplicated in each command.

## Config merge (for extenders)

Later wins; partial YAML is fine.

| Layer | Source |
|-------|--------|
| 1 | `default.yaml` embedded in the binary (`main.go`) |
| 2 | XDG `…/agctl/config.yaml` if it exists |
| 3 | `--config` / `AGCTL_CONFIG` if set (file must exist) |
| 4 | `AGCTL_*` env vars |
| 5 | Chat/run flags (`ApplyAgentOverrides`) |

**Adding a new setting**

1. Document the default in `default.yaml`.
2. Add the field to `config.Config` (and nested structs as needed).
3. Fill defaults in `ensureConfigStructs`; add env keys in `applyEnvOverrides` if users should override via env.
4. Wire it in `internal/agent.Build` (or a small helper there).
5. If `agctl config show` should reflect it, update `ForShow` (redact secrets, hide sections that do not apply).

**Two loaders:** `LoadConfig` is the full merge stack the agent uses. Kong-yaml (`kongYAMLPath` in `cli.go`) may seed **flag defaults** from one existing file only — do not treat it as a second source of truth.

**`config edit`** copies embedded `default.yaml` into a temp file, opens `$EDITOR`, and writes to XDG on save (or to `--config` when that path is explicit).

## New subcommand

1. Add a type with `Run(...)` under `internal/command/` — copy the pattern from `run.go` or `config.go`.
2. Register it on `CLI` in `cli.go` with `` `cmd:""` `` and help text.
3. If `Run` needs injected values, add `ctx.Bind(...)` in `Execute` (today: `*config.Config`, version, config path, embedded YAML bytes).

Chat and run share runtime/LLM/temporal flags through **`AgentOverrides`** embedded on both structs. Extend that embed instead of copying Kong tags.

## Tests and release

Tests live next to packages: `internal/config`, `internal/agent`, `cli_test.go`, etc.

```bash
go test ./... -count=1
```

The eval harness under `../eval-harness/` evaluates SDK agent behavior, not the CLI binary — no hook there unless you add one on purpose.

Release binaries are built by GoReleaser on git tags (`dir: ./cli` in [`.goreleaser.yaml`](../.goreleaser.yaml)); tags set `main.version` so `agctl version` matches the release. Maintainer flow: [RELEASING.md](../RELEASING.md).

User-facing install and usage: [root README](../README.md) and [CLI docs](https://docs.agenticenv.ai/getting-started/cli).
