# Agent SDK for Go

[![CI](https://github.com/agenticenv/agent-sdk-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/agenticenv/agent-sdk-go/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/agenticenv/agent-sdk-go?label=Release)](https://github.com/agenticenv/agent-sdk-go/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/agenticenv/agent-sdk-go.svg)](https://pkg.go.dev/github.com/agenticenv/agent-sdk-go)
[![License](https://img.shields.io/github/license/agenticenv/agent-sdk-go?label=License)](LICENSE)
[![Mentioned in Awesome Go](https://awesome.re/mentioned-badge.svg)](https://github.com/avelino/awesome-go)

**AI agents in Go that keep running even when your process doesn't — powered by [Temporal](https://temporal.io) or [Restate](https://restate.dev).**

**Open-source Go SDK for building AI agents** — run in-process with zero setup, or switch to Temporal / Restate for crash-resilient, distributed execution that survives restarts and deploys. Every core component is a pluggable interface, so nothing is locked in.

📖 [Documentation](https://docs.agenticenv.ai)  ·  [Quickstart](https://docs.agenticenv.ai/getting-started/quickstart)  ·  [Examples](https://docs.agenticenv.ai/examples/running-examples) 

> Releases follow [Semantic Versioning](https://semver.org/); see the [latest release](https://github.com/agenticenv/agent-sdk-go/releases/latest).
>
> Independent community library — **not** affiliated with Temporal Technologies or Restate.

## Install

```bash
go get github.com/agenticenv/agent-sdk-go@latest
```

Go 1.26+. No infrastructure required for in-process mode. A running [Temporal](https://temporal.io) or [Restate](https://restate.dev) server is required for durable execution — see [temporal-setup.md](temporal-setup.md) and [restate-setup.md](restate-setup.md).

## Quick Start

**In-process** (zero setup):

```go
import (
    "context"
    "fmt"
    "time"

    "github.com/agenticenv/agent-sdk-go/pkg/agent"
    "github.com/agenticenv/agent-sdk-go/pkg/llm"
    "github.com/agenticenv/agent-sdk-go/pkg/llm/openai"
)

// errors omitted for brevity
llmClient, _ := openai.NewClient(
    llm.WithAPIKey("sk-..."),
    llm.WithModel("gpt-4o"),
)

a, _ := agent.NewAgent(
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(llmClient),
)
defer a.Close()

// --- Run ---
run, _ := a.Run(context.Background(), "Reply with a short greeting.", nil)
result, _ := run.Get(context.Background())
fmt.Println(result.Content)

// --- Non-blocking ---
run, _ = a.Run(context.Background(), "Explain durable agents in two short paragraphs.", nil)
select {
case <-run.Done():
    result, _ = run.Get(context.Background())
    fmt.Println(result.Content)
case <-time.After(5 * time.Second):
    fmt.Println("still running, check back later")
}

// --- Stream (AG-UI events: text deltas, tools, approvals, lifecycle, …) ---
stream, _ := a.Stream(context.Background(), "Write a four-line poem about the ocean.", nil)
events, _ := stream.Events(context.Background())
for event := range events {
    switch e := event.(type) {
    case *agent.AgentTextMessageContentEvent:
        fmt.Print(e.Delta)
    case *agent.AgentToolCallStartEvent:
        fmt.Println("\n[tool call]", e.ToolCallName)
    case *agent.AgentCustomEvent:
        // tool / delegation approval (when approval policy requires it)
        if e.Name == string(agent.AgentCustomEventNameToolApproval) {
            if v, err := agent.ParseCustomEventApproval(e); err == nil {
                // replace with real approval logic — this auto-approves for demonstration
                _ = stream.Approve(context.Background(), v.ApprovalToken, agent.ApprovalStatusApproved)
            }
        }
    // also RunFinished, ToolCallResult, …
    }
}
```

**Temporal** (durable execution) — import `pkg/agent/runtime/temporal`:

```go
import "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/temporal"

a, _ := agent.NewAgent(
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(llmClient),
    temporal.WithTemporalConfig(&temporal.TemporalConfig{
        Host:      "localhost",
        Port:      7233,
        Namespace: "default",
        TaskQueue: "agent-task-queue",
    }),
)
defer a.Close()

// --- Run ---
run, _ := a.Run(context.Background(), "Reply with a short greeting.", nil)
result, _ := run.Get(context.Background())
fmt.Println(result.Content)

// --- Stream + reconnect ---
stream, _ := a.Stream(context.Background(), "Write a four-line poem about the ocean.", nil)
savedRunID := stream.ID() // persist before consuming events
events, _ := stream.Events(context.Background())
for event := range events {
    // persist event.Offset() before handling — needed for WithOffset on reconnect
    _ = event
}
savedOffset := int64(0)
s, _ := a.GetAgentStream(context.Background(), savedRunID)
ch, _ := s.Events(context.Background(), agent.WithOffset(savedOffset))
for event := range ch {
    _ = event
}
```

**Restate** (durable execution) — import `pkg/agent/runtime/restate` (mutually exclusive with Temporal):

```go
import "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate"

a, _ := agent.NewAgent(
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(llmClient),
    restate.WithRestateConfig(&restate.RestateConfig{
        Ingress: restate.IngressConfig{
            URL: "http://localhost:8080",
        },
        Endpoint: restate.EndpointConfig{
            ListenAddress: ":9080",
            AdminURL:      "http://localhost:9070",
        },
    }),
)
defer a.Close()

// Same Run / Stream / GetAgentStream + WithOffset APIs as Temporal
```

> Crashes and process restarts don't have to mean lost work or missed approvals — see [durable_agent/temporal](examples/durable_agent/temporal) (split worker) and [durable_agent/restate](examples/durable_agent/restate) (single process). For the stream reconnect protocol (`GetAgentStream` + `WithOffset`), see the [reconnect example](examples/agent_with_reconnect) and [Durable Execution](https://docs.agenticenv.ai/advanced/durable-execution).

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
- **Durable execution** — crash-resilient runs via Temporal or Restate; reconnect to active runs and resume event streams after a restart
- **Distributed execution** — with Temporal, decouple client triggers from worker execution across processes; with Restate, scale via registered endpoint deployments
- **Observability** — OpenTelemetry traces, metrics, and structured logs

## CLI (`agctl`)

Download a binary from [GitHub Releases](https://github.com/agenticenv/agent-sdk-go/releases), extract it, and put `agctl` on your `PATH`.

```bash
export AGCTL_LLM_APIKEY=sk-your-key
agctl run --model gpt-4o --prompt "hello"
# or interactive
agctl chat
```

See the [CLI docs](https://docs.agenticenv.ai/getting-started/cli) for commands, config, and env vars.

## Reference Apps

- **[Agent Chat](https://github.com/agenticenv/agent-chat)** — web chat demo with durable conversations; reference for wiring the SDK into an HTTP-backed app.

![Agent Chat Demo](https://raw.githubusercontent.com/agenticenv/agent-chat/main/demo.gif)

## Examples

Runnable examples in [examples/](examples/) — see [examples/README.md](examples/README.md) for setup and run instructions.

## Benchmarks

Config-driven benchmark runner — see [benchmarks/README.md](benchmarks/README.md)

## Eval Harness

Evaluate agent quality with Promptfoo and DeepEval — locally or in CI. See [eval-harness/README.md](eval-harness/README.md)

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, workflow, and guidelines.
Project policies: [SECURITY.md](SECURITY.md) · [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

Quick commands (requires [Task](https://taskfile.dev)): `task check` | `task test` | `task lint` | `task fmt` | `task tidy` | `task test-coverage`

Coverage reports (PR and default branch) are on **[Codecov](https://app.codecov.io/gh/agenticenv/agent-sdk-go)**. Run `task test-coverage` locally to produce `coverage.out` and `coverage.html`.

## License

[Apache 2.0](LICENSE)

## Disclaimer

This project is provided "as is" under the Apache License 2.0. When building AI agents that execute real-world actions, ensure appropriate safeguards, validation, and human-in-the-loop approval workflows are in place. You are responsible for compliance, access control, and operational safety in your deployment. For security issues, follow [SECURITY.md](SECURITY.md).