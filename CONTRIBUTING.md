# Contributing to agent-sdk-go

Thank you for your interest in contributing. **agent-sdk-go** is a community Go SDK for AI agents that **run on the [Temporal](https://temporal.io)** runtime (workflows and activities). You need a **running Temporal server** for examples and the CLI; see **[temporal-setup.md](temporal-setup.md)**. This document explains how to set up your environment and what we expect from contributors.

## Prerequisites

Before contributing, ensure you have:

| Requirement | Version / Notes |
|-------------|-----------------|
| **Go** | **Minimum `go 1.24.0`** (see the `go` line in `go.mod`; use that version or newer). |
| **Temporal server** | Required for examples, CLI, and tests — see [Temporal setup](temporal-setup.md) |
| **golangci-lint** | Required for `make lint` — install with the **same Go as `go.mod`**: `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` |
| **gofmt** | `make lint` runs `gofmt -s` check first; run `make fmt` to apply `gofmt -s -w` project-wide |
| **misspell** | `make spell` or `make lint` — typos via `misspell` (similar to Go Report Card) |

## Temporal setup

Run Temporal locally (or point at Cloud/self-hosted) before examples and tests. Full steps: **[temporal-setup.md](temporal-setup.md)**.

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

### 3. Run tests

```bash
make test
```

**CI runs automatically** on pull requests and on pushes to branches other than `main`. Pushes or merges to `main` do not trigger CI automatically; use **workflow_dispatch** in GitHub Actions when you need an on-demand run. Lint and test must pass before merge — fix any CI failures in your PR.

Or run tests for a specific package:

```bash
go test ./pkg/agent/... -count=1 -v
```

### 4. Run linters

```bash
make lint
```

This runs `go vet` and `golangci-lint`. All contributions must pass lint with zero errors.

**golangci-lint vs Go version:** If you see `the Go language version used to build golangci-lint is lower than the targeted Go version`, reinstall the linter with your current Go: `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`. The binary must be built with Go **≥** the `go` line in `go.mod`.

### 5. Generate coverage

```bash
make test-coverage
# Open coverage.html in a browser
```

### 6. Run examples (optional)

Copy the env sample and set your LLM API key:

```bash
cp examples/env.sample examples/.env
# Edit examples/.env: set LLM_APIKEY, LLM_MODEL
```

Then run any example:

```bash
go run ./examples/simple_agent "Hello"
```

See [examples/README.md](examples/README.md) for all examples and env vars.

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
   - Run `make lint` and `make test` before submitting a PR. PRs must pass both.
   - Run `make tidy` before committing if you add or remove dependencies.

2. **Tests**
   - Add tests for new features and bug fixes.
   - Unit tests go in `*_test.go` files alongside the code.

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

## Project Layout

| Path | Purpose |
|------|---------|
| `pkg/agent/` | Agent core, workflow, config |
| `pkg/llm/` | LLM providers (OpenAI, Anthropic, Gemini) |
| `pkg/interfaces/` | Interfaces for LLM clients, tools, messages |
| `pkg/tools/` | Built-in and custom tools |
| `pkg/conversation/` | Message history (in-memory, Redis) |
| `cmd/` | agentctl CLI |
| `examples/` | Example programs |

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
