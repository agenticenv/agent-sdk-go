# Agent SDK for Go

[![CI](https://github.com/agenticenv/agent-sdk-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/agenticenv/agent-sdk-go/actions)
[![Release](https://img.shields.io/github/v/release/agenticenv/agent-sdk-go?label=Release)](https://github.com/agenticenv/agent-sdk-go/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/agenticenv/agent-sdk-go.svg)](https://pkg.go.dev/github.com/agenticenv/agent-sdk-go)
[![License](https://img.shields.io/github/license/agenticenv/agent-sdk-go?label=License&cacheSeconds=0)](LICENSE)
[![Mentioned in Awesome Go](https://awesome.re/mentioned-badge.svg)](https://github.com/avelino/awesome-go)

**Open-source Go SDK for building production-grade AI agents** — extensible and pluggable by design.
Run in-process with zero setup, or on [Temporal](https://temporal.io) for durable, crash-resilient production execution.

📖 [Documentation](https://docs.agenticenv.ai)  ·  [Quickstart](https://docs.agenticenv.ai/getting-started/quickstart)  ·  [Examples](https://docs.agenticenv.ai/examples/running-examples) 

> **Versioning:** [Semantic versioning](https://semver.org/); releases are git tags. See the [latest release](https://github.com/agenticenv/agent-sdk-go/releases/latest).
>
> Independent community library — **not** affiliated with Temporal Technologies.



## Install

```bash
go get github.com/agenticenv/agent-sdk-go@latest
```

Go 1.26+. No infrastructure required for in-process mode. A running [Temporal](https://temporal.io) server is required for durable execution.

## Quick Start

**In-process** (zero setup):

```go
import (
    "context"
    "fmt"
    "github.com/agenticenv/agent-sdk-go/pkg/agent"
    "github.com/agenticenv/agent-sdk-go/pkg/llm"
    "github.com/agenticenv/agent-sdk-go/pkg/llm/openai"
)

llmClient, _ := openai.NewClient(
    llm.WithAPIKey("sk-..."),
    llm.WithModel("gpt-4o"),
)

a, _ := agent.NewAgent(
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(llmClient),
)
defer a.Close()

result, _ := a.Run(context.Background(), "Hello", nil)
fmt.Println(result.Content)
```

**Temporal** (durable, production):

```go
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(&agent.TemporalConfig{
        Host: "localhost", Port: 7233,
        Namespace: "default", TaskQueue: "my-app",
    }),
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(llmClient), // same llmClient as above
)
defer a.Close()

result, _ := a.Run(context.Background(), "Hello", nil)
fmt.Println(result.Content)
```



## Features

- **LLM providers** — OpenAI, Anthropic, Gemini, DeepSeek, Ollama (local) + custom via `interfaces.LLMClient`
- **Tools & MCP** — built-in and custom tools; MCP servers over stdio or streamable HTTP
- **A2A** — expose agents as A2A servers or connect remote A2A agents as tools
- **Sub-agents** — delegate to specialist agents with independent LLMs, tools, and task queues
- **Human-in-the-loop approvals** — gate tool calls, MCP invocations, and delegation
- **Conversation history** — multi-turn sessions via in-memory or Redis backends
- **Memory & RAG** — long-term scoped memory and retrieval-augmented generation
- **Streaming & AG-UI** — partial token streaming; AG-UI protocol for frontend integration
- **Reasoning** — extended thinking on Anthropic, Gemini, DeepSeek, and OpenAI reasoning models
- **Token usage** — aggregate prompt, completion, and reasoning token counts per run
- **Hooks & guardrails** — middleware at LLM, tool, retrieval, and memory lifecycle points
- **Execution config** — per-operation timeouts and max attempts via `With*ExecutionConfig`
- **Durable execution** — crash-resilient runs via Temporal; horizontal worker scaling
- **Observability** — OpenTelemetry traces, metrics, and structured logs



## Reference Apps

- **[Agent Chat](https://github.com/agenticenv/agent-chat)** — web chat demo with durable conversations; reference for wiring the SDK into an HTTP-backed app.



## Examples

Runnable examples in `[examples/](examples/)` — see `[examples/README.md](examples/README.md)` for setup and run instructions.

## Benchmarks

Config-driven benchmark runner — see [benchmarks/README.md](benchmarks/README.md)

## Eval Harness

Evaluate agent quality with Promptfoo and DeepEval — locally or in CI. See [eval-harness/README.md](eval-harness/README.md)

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, workflow, and guidelines.
Project policies: [SECURITY.md](SECURITY.md) · [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

Quick commands: `make check` | `make test` | `make lint` | `make fmt` | `make tidy` | `make test-coverage`

Coverage reports (PR and default branch) are on **[Codecov](https://app.codecov.io/gh/agenticenv/agent-sdk-go)**. Run `make test-coverage` locally to produce `coverage.out` and `coverage.html`.

## License

[Apache 2.0](LICENSE)

## Disclaimer

This project is provided "as is" under the Apache License 2.0. When building AI agents that execute real-world actions, ensure appropriate safeguards, validation, and human-in-the-loop approval workflows are in place. You are responsible for compliance, access control, and operational safety in your deployment. For security issues, follow [SECURITY.md](SECURITY.md).