# Contributing to agent-sdk-go

Thank you for your interest in contributing. **agent-sdk-go** is a community Go SDK for AI agents — backed by [Temporal](https://temporal.io) for durable execution, or running in-process with no external dependencies. This document explains how to set up your environment and what we expect from contributors.

## Prerequisites

Before contributing, ensure you have:

| Requirement | Version / Notes |
|-------------|-----------------|
| **Go** | **Minimum `go 1.26.0`** (see the `go` line in `go.mod`; use that version or newer). |
| **Task** | Task runner for all dev commands (`task build`, `task check`, `task lint`, ...). Install: `go install github.com/go-task/task/v3/cmd/task@latest` or see [taskfile.dev/installation](https://taskfile.dev/installation/) |
| **Temporal server** | Required only for Temporal runtime examples, CLI, and Temporal-specific tests — see [Temporal setup](temporal-setup.md). Unit tests and in-process runtime examples run without it. |
| **golangci-lint** | Required for `task lint` — install **v2** with Go **≥** the `go` line in `go.mod`: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` |
| **gofmt** | `task lint` runs `gofmt -s` check first; run `task fmt` to apply `gofmt -s -w` project-wide |
| **misspell** | `task spell` or `task lint` — typos via `misspell` |

## Temporal setup

Only needed for the **Temporal runtime** path — examples and tests that use `AGENT_RUNTIME=temporal` or `WithTemporalConfig`. In-process runtime examples and unit tests run without it. Full steps: **[temporal-setup.md](temporal-setup.md)**.

## Development Workflow

### 1. Clone and prepare

**Fork** the repo on GitHub (if you don't have push access), then clone your fork:

```bash
git clone https://github.com/<your-username>/agent-sdk-go.git
cd agent-sdk-go
git remote add upstream https://github.com/agenticenv/agent-sdk-go.git
go mod download
```

(If you have push access, you may clone the main repo and create branches there.)

### 2. Create a branch for your changes

Create a branch from `main` for each change. Do not push directly to `main`.

```bash
git checkout main
git pull upstream main    # or origin main if using main repo
git checkout -b <branch-name>
```

**Branch naming** (common open source practice):

| Prefix | Use for |
|--------|---------|
| `feat/` | New features (e.g. `feat/add-retry`, `feat/streaming-improvements`) |
| `fix/` | Bug fixes (e.g. `fix/nil-pointer`, `fix/timeout-handling`) |
| `docs/` | Documentation only (e.g. `docs/readme`, `docs/api-examples`) |
| `test/` | Test additions or fixes (e.g. `test/llm-provider`) |
| `refactor/` | Code refactoring, no behavior change |
| `chore/` | Maintenance (deps, tooling, config) |

Keep your branch short and descriptive. Sync with `main` before opening a PR: `git pull upstream main` (or rebase if you prefer). Push your branch to your fork and open a PR against `main`.

### 3. Run checks before a PR

```bash
task check
```

Runs `fmt-check`, spell check, `task lint`, `task test`, `task build`, and `task secrets-scan` — same core gates as the main CI job (coverage is CI-only; use `task test-coverage` locally if you want a report). `task test` includes eval-harness Go tests; the full Promptfoo/DeepEval suite runs in CI and via `task eval-harness` (see below).

Also run the full example suite on any code change to catch regressions unit tests may miss:

```bash
task examples:all
```

Requires Task, Docker, and LLM credentials — see [examples/README.md](examples/README.md).

If you change **agent behavior** (e.g. `pkg/agent`, `pkg/memory`, telemetry, tools, runtime) or **`eval-harness/`**, run:

```bash
task eval-harness
```

Behavioral regression tests use mock LLM/tools and assert on run output — SDK changes can break them even when eval-harness files are untouched. Requires Node.js and Python 3.10+ — see [eval-harness/README.md](eval-harness/README.md). CI runs this automatically on PRs (`eval-harness` job).

**CI runs automatically** on pull requests to `main` (open a PR or push updates to an existing PR to re-run checks). Pushes or merges to `main` do not trigger CI; use **workflow_dispatch** in GitHub Actions for an on-demand run. Run `task check` locally before opening a PR; CI must pass on the PR before merge.

To run only tests (e.g. while iterating):

```bash
task test
```

Or a specific package:

```bash
go test ./pkg/agent/... -count=1 -v
```

### 4. Run linters (included in `task check`)

```bash
task lint
```

This runs `gofmt -s` check, `misspell`, `go vet`, and `golangci-lint`. Use when debugging a lint failure without re-running the full `task check`.

**golangci-lint vs Go version:** If you see `the Go language version used to build golangci-lint is lower than the targeted Go version`, your `golangci-lint` binary is too old for this module (Go 1.26+ requires **golangci-lint v2**). Reinstall: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`, ensure `$(go env GOPATH)/bin` is on `PATH` ahead of any older install, then run `golangci-lint version` — it should report **v2.x** and a Go build **≥ 1.26**.

### 5. Generate coverage

```bash
task test-coverage
# Open coverage.html in a browser
```

### 6. Run examples

Examples load **`examples/.env.defaults`** automatically. Set LLM credentials via environment or an optional override file:

```bash
export LLM_APIKEY=your-key
export LLM_PROVIDER=your-provider
export LLM_MODEL=your-model
# LLM_PROVIDER: openai, anthropic, or gemini. Or append the same keys to examples/.env
```

Run the full example suite before a PR (local + temporal, with reports):

```bash
task examples:all
```

When you add a new example, register it in **`taskfiles/examples.yml`** if it can run non-interactively via Task (one-shot `go run`, no REPL, no split worker process). See existing lists (`EXAMPLES`, `EXAMPLES_WITH_PROMPTS`, `EXAMPLES_TEMPORAL`) and commented TODOs for patterns.

Or run a single example:

```bash
go run ./examples/simple_agent "Hello"
```

See [examples/README.md](examples/README.md) for all examples, env vars, Task install, and infra commands (`task infra:*`, `task examples:local`). Memory examples (`examples/agent_with_memory/`) need Weaviate or pgvector — see [examples/agent_with_memory/README.md](examples/agent_with_memory/README.md).

## Ways to Contribute

### Propose a feature

Before implementing a new feature, **open an issue** to propose and discuss it. This helps:

- Align on scope and design before you spend time coding
- Avoid duplicate work if someone else is already working on it
- Get feedback from maintainers early

Use the **Feature** or **Enhancement** label if available, and include: use case, proposed API or behavior, and any alternatives you considered.

### Report bugs

Found a bug? **Open an issue** with:

- Steps to reproduce
- Expected vs actual behavior
- Go version, OS, and (if relevant) Temporal and LLM provider versions
- Minimal code or config that reproduces the problem

### Share testing feedback

Using the SDK and ran into issues, unclear docs, or confusing behavior? **Raise an issue** even if you’re not sure it’s a bug. Testers and early adopters are valuable; include as much context as you can (version, setup, what you tried).

### Code contributions

1. **Discuss first** for larger changes — open an issue or discussion before a big PR.
2. **Small fixes** (typos, docs, obvious bugs) can go directly to a PR.
3. **Pull requests** — see [What Contributors Must Follow](#what-contributors-must-follow) below.

## What Contributors Must Follow

1. **Code quality**
   - Run `task check` before submitting a PR (format, spell, lint, test, build, secrets scan). PRs must pass.
   - Run `task examples:all` before submitting a PR to verify nothing in the example suite breaks (any code change — not only example edits). Requires Task, Docker, and LLM credentials — see [examples/README.md](examples/README.md).
   - New examples that support batch runs must be added to **`taskfiles/examples.yml`** (see §6).
   - Run `task tidy` before committing if you add or remove dependencies.

2. **Tests**
   - Add tests for new features and bug fixes.
   - Unit tests go in `*_test.go` files alongside the code.
   - Agent behavior changes (`pkg/agent`, `pkg/memory`, telemetry, tools, runtime) or **`eval-harness/`** edits — run `task eval-harness` before submitting a PR.

3. **Commits**
   - Use [conventional commits](https://www.conventionalcommits.org) — these drive the release changelog:
     - `feat: add streaming support` — features
     - `fix: handle nil pointer in config` — bug fixes
     - `docs: update README examples` — documentation
     - `test: add unit tests for agent` — tests
     - `ci: update release workflow` — CI/CD
     - `chore: bump dependencies` — maintenance
   - Prefer one logical change per commit.

4. **Pull requests**
   - Open a PR against the default branch.
   - Describe the change and why it's needed.
   - Reference any related issues.

5. **Scope**
   - Keep changes focused. For larger work, consider splitting into multiple PRs.
   - For new LLM providers: implement `interfaces.LLMClient` (see `pkg/interfaces/llm.go` and existing providers in `pkg/llm/`).
   - For new tools: implement `interfaces.Tool` (see `pkg/interfaces/tools.go` and `pkg/tools/`).
   - For new memory backends: implement `interfaces.Memory` (see `pkg/interfaces/memory.go` and `pkg/memory/weaviate` or `pkg/memory/pgvector`).

## Releasing (maintainers only)

See **[RELEASING.md](RELEASING.md)** for how to cut releases — tag-triggered workflow, checklist, and version rules.

## Getting Help

| Need | Where |
|------|-------|
| **Feature idea or design discussion** | [Open an issue](https://github.com/agenticenv/agent-sdk-go/issues) (use Feature/Enhancement label if available) |
| **Bug report** | [Open an issue](https://github.com/agenticenv/agent-sdk-go/issues) with repro steps |
| **Question or general discussion** | [GitHub Discussions](https://github.com/agenticenv/agent-sdk-go/discussions) |
| **Security concern** | See [SECURITY.md](SECURITY.md) |

We follow typical open source flow: discuss in issues/discussions first for non-trivial changes, then implement and open a PR when ready.
