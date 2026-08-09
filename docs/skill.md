---
name: agent-sdk-go
description: Build production AI agents in Go with Agent SDK for Go. Use when configuring agents, tools, MCP, A2A, Temporal or Restate runtimes, streaming, memory, RAG, approvals, observability, or running examples from this documentation site.
version: "1.0"
compatibility: Go 1.26+. LLM API key required (OpenAI, Anthropic, or Gemini). Temporal or Restate optional for durable execution.
---

# Agent SDK for Go

Go library for building production AI agents — LLM calls, tools, multi-turn conversation, memory, RAG, MCP and A2A integrations, human-in-the-loop approvals, and sub-agent delegation.

Full documentation index: [llms.txt](https://docs.agenticenv.ai/llms.txt)

## Capabilities

- Create and configure agents with `NewAgent` and functional options
- Run agents with `Run` (handle → `Get` / `Done`) or `Stream` (handle → `Events`)
- Register built-in, custom, MCP, and A2A tools
- Persist conversation history (in-memory or Redis)
- Store and recall long-term memory (Weaviate, pgvector)
- Add RAG retrievers (agentic, prefetch, hybrid modes)
- Require human approval for tools and sub-agent delegation
- Attach middleware hooks at LLM, tool, retrieval, and memory lifecycle points
- Export OpenTelemetry traces, metrics, and logs
- Execute in-process, or on Temporal / Restate for durable runs

## Workflows

### Create a minimal agent

1. Read [Quickstart](https://docs.agenticenv.ai/getting-started/quickstart.md)
2. Configure an LLM client — [LLM Providers](https://docs.agenticenv.ai/getting-started/llm-providers.md)
3. Call `NewAgent` with `WithLLMClient` and `WithSystemPrompt`
4. Call `Run(ctx, prompt, nil)`, then `Get(ctx)` on the returned handle for `AgentRunResult`
5. Always call `defer a.Close()` — required to flush OTLP exporters on shutdown

Example: [Simple Agent](https://docs.agenticenv.ai/examples/simple-agent.md)

### Add tools

1. Read [Tools](https://docs.agenticenv.ai/features/tools.md)
2. Register tools with `WithTools` or `WithToolRegistry`
3. Set `WithToolApprovalPolicy(AutoToolApprovalPolicy())` for trusted automation
4. Run: [Tools example](https://docs.agenticenv.ai/examples/tools.md)

### Stream to a UI

1. Read [Streaming](https://docs.agenticenv.ai/getting-started/streaming.md)
2. Call `Stream`, then `Events(ctx)` on the handle and consume `<-chan AgentEvent`
3. Check for `nil` events; handle `RUN_FINISHED` for final result and token usage
4. Example: [Stream](https://docs.agenticenv.ai/examples/stream.md) · [AG-UI](https://docs.agenticenv.ai/examples/agui.md)

### Switch to Temporal (durable execution)

1. Read [Temporal runtime](https://docs.agenticenv.ai/runtimes/temporal.md)
2. Import `pkg/agent/runtime/temporal` and add `temporal.WithTemporalConfig` or `temporal.WithTemporalClient` — never both
3. For production, split client and worker — [Distributed execution](https://docs.agenticenv.ai/advanced/distributed-execution.md)
4. Align agent and worker configuration (fingerprint) — same name, LLM, tools, hooks group names, approval policy
5. Examples: [Temporal Client](https://docs.agenticenv.ai/examples/temporal-client.md) · [Agent Worker](https://docs.agenticenv.ai/examples/agent-worker.md) · [Durable Agent (Temporal)](https://docs.agenticenv.ai/examples/durable-agent.md) · [Durable Agent (Restate)](https://docs.agenticenv.ai/examples/durable-agent-restate.md)

### Switch to Restate (durable execution)

1. Read [Restate runtime](https://docs.agenticenv.ai/runtimes/restate.md)
2. Import `pkg/agent/runtime/restate` and add `restate.WithRestateConfig`
3. Use `restate.WithRestateConfig` alone (mutually exclusive with Temporal options); `NewAgent` embeds the SDK endpoint
4. When Restate runs in Docker and the agent on the host, set `Endpoint.DeploymentURL` (e.g. `http://host.docker.internal:9080`)
5. Local setup: [restate-setup.md](https://github.com/agenticenv/agent-sdk-go/blob/main/restate-setup.md) · lab: [Durable Agent (Restate)](https://docs.agenticenv.ai/examples/durable-agent-restate.md) · `AGENT_RUNTIME=restate` + `task infra:restate:up`

### Reconnect after crash (Temporal or Restate)

Crash reconnect requires a **durable runtime** (Temporal or Restate). `LocalRuntime` handles are same-process; after a crash, `GetAgentRun` / `GetAgentStream` return `ErrRunNotFound` / `ErrStreamNotFound`, and `WithOffset(n>0)` returns `ErrStreamOffsetNotSupported`.

1. Read [Durable Execution](https://docs.agenticenv.ai/advanced/durable-execution.md) and [Temporal](https://docs.agenticenv.ai/runtimes/temporal.md) or [Restate](https://docs.agenticenv.ai/runtimes/restate.md)
2. Persist the handle `ID()` immediately after `Run` / `Stream` — before `Get` / consuming `Events`
3. For streams, persist each event’s opaque offset **before** handling the event
4. On restart — stream: `GetAgentStream(ctx, savedRunID)` then `Events(ctx, agent.WithOffset(savedOffset))`
5. On restart — run: `GetAgentRun(ctx, savedRunID)` then `Get(ctx)` (or `<-Done()` then `Get`)
6. If the run already finished: `ErrRunAlreadyCompleted` — clear saved state; continue from conversation/memory
7. Cancelling `Run`/`Stream` ctx cancels the agent run; cancelling `Get`/`Events`/`GetAgentRun`/`GetAgentStream` ctx does not — call `Cancel()` on the handle to stop the run. After reconnect, `WithTimeout` starts fresh (not remaining time). Details: [Timeouts & Modes](https://docs.agenticenv.ai/advanced/timeouts-and-modes.md)
8. Examples: [Reconnect](https://docs.agenticenv.ai/examples/reconnect.md) · [Durable Agent (Temporal)](https://docs.agenticenv.ai/examples/durable-agent.md) · [Durable Agent (Restate)](https://docs.agenticenv.ai/examples/durable-agent-restate.md)

## Integration

| Component | Documentation |
|---|---|
| LLM providers | [LLM Providers](https://docs.agenticenv.ai/getting-started/llm-providers.md) |
| All agent options | [Configuration](https://docs.agenticenv.ai/getting-started/configuration.md) |
| In-process runtime | [In-Process](https://docs.agenticenv.ai/runtimes/in-process.md) |
| Temporal runtime | [Temporal](https://docs.agenticenv.ai/runtimes/temporal.md) |
| Restate runtime | [Restate](https://docs.agenticenv.ai/runtimes/restate.md) |
| MCP servers | [MCP](https://docs.agenticenv.ai/features/mcp.md) |
| A2A remote agents | [A2A](https://docs.agenticenv.ai/features/a2a.md) |
| Observability | [Telemetry](https://docs.agenticenv.ai/observability/telemetry.md) |
| Runnable examples | [Running Examples](https://docs.agenticenv.ai/examples/running-examples.md) |
| Go API reference | https://pkg.go.dev/github.com/agenticenv/agent-sdk-go |

## Context

- Architecture: [Architecture](https://docs.agenticenv.ai/architecture.md)
- Agent loop: prepare context → call LLM → execute tools (iterate) → finalize
- Capabilities resolve at call time from registries — tools, MCP, A2A, and sub-agents can change between runs
- Feature pages explain concepts; example pages show run commands and expected output under `examples/`
- Default tool approval policy is **require-all** — set `AutoToolApprovalPolicy()` for unattended runs
- `DisableLocalWorker()` works with streaming and approvals with no extra configuration
- Hook group **names** participate in the Temporal agent fingerprint — register the same names on client and worker

## Documentation map

| Section | Entry point |
|---|---|
| Overview | [Introduction](https://docs.agenticenv.ai/introduction.md) |
| Getting started | [Quickstart](https://docs.agenticenv.ai/getting-started/quickstart.md) |
| Features | [Tools](https://docs.agenticenv.ai/features/tools.md) |
| Advanced | [Distributed execution](https://docs.agenticenv.ai/advanced/distributed-execution.md) |
| Observability | [Telemetry](https://docs.agenticenv.ai/observability/telemetry.md) |
| Examples | [Running Examples](https://docs.agenticenv.ai/examples/running-examples.md) |
| Production | [Readiness](https://docs.agenticenv.ai/production/readiness.md) |
