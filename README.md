# AI Agent SDK for Go, Powered by Temporal

[![CI](https://github.com/vvsynapse/agent-sdk-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/vvsynapse/agent-sdk-go/actions)
[![Release](https://img.shields.io/github/v/release/vvsynapse/agent-sdk-go?label=Release)](https://github.com/vvsynapse/agent-sdk-go/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/vvsynapse/agent-sdk-go.svg)](https://pkg.go.dev/github.com/vvsynapse/agent-sdk-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/vvsynapse/agent-sdk-go)](https://goreportcard.com/report/github.com/vvsynapse/agent-sdk-go)
[![License](https://img.shields.io/github/license/vvsynapse/agent-sdk-go?label=License)](LICENSE)

Build durable, long-running AI agents in Go — tool calling, human approvals, and sub-agent delegation, powered by **[Temporal](https://temporal.io)**.

> **Note:** Independent community library — **not** affiliated with Temporal Technologies.
>
> **Runtime:** Requires a running Temporal cluster — [self-hosted](https://docs.temporal.io/self-hosted-guide) or [Temporal Cloud](https://temporal.io/cloud).
>
> **Version:** `v0.0.10` — Active development. Follows [semantic versioning](https://semver.org/); API may evolve before v1.0.0.

## Overview

Use this SDK when you want **LLM-driven agents** (tools, optional specialists) whose runs **survive worker restarts** and can last a long time: orchestration, retries, timeouts, child workflows for delegation, and approval pauses come from **Temporal**, with history and debugging in the **Temporal UI**. Typical in-process agent loops are replaced by **replay-safe workflow code** plus activities for side effects.

**Why wire agents to Temporal?**

- Reliable tool execution (retries, failure recovery around activities)
- Human-in-the-loop gates before tools or sub-agent delegation
- Multi-step and multi-agent flows (task queues, child workflows)
- Long-running runs without losing progress (durable workflow state)
- Traceability (replay, visibility in Temporal)

## Capabilities

- **LLM integration** — OpenAI, Anthropic, and Gemini with tool/function calling
- **Streaming** — Partial content and thinking deltas via **RunStream**
- **Tools** — Built-in tools and custom tools via **interfaces.Tool**
- **Approval gates** — Optional human-in-the-loop approval before executing tools or delegating to sub-agents
- **Sub-agents** — Delegate work to specialist agents you register
- **Durable execution** — Agents survive restarts, run for minutes to days without losing state
- **Distributed execution** — Agents run across **agent workers** and activities, horizontally scalable across multiple instances.
- **Temporal runtime** — Agents run as workflows; durable execution, retries, and UI visibility come from Temporal

## Getting started

Prerequisites: a running [Temporal](https://docs.temporal.io/self-hosted-guide) environment (required — agents do not run without it), Go 1.21+, and credentials for whatever LLM you plug in.

**Module:** `github.com/vvsynapse/agent-sdk-go`

```bash
go get github.com/vvsynapse/agent-sdk-go@latest
```

### Create an agent and run

```go
import (
    "github.com/vvsynapse/agent-sdk-go/pkg/agent"
    "github.com/vvsynapse/agent-sdk-go/pkg/llm"
    "github.com/vvsynapse/agent-sdk-go/pkg/llm/openai"
)

llmClient, _ := openai.NewClient(
    llm.WithAPIKey("sk-..."),
    llm.WithModel("gpt-4o"),
)

a, _ := agent.NewAgent(
    agent.WithTemporalConfig(&agent.TemporalConfig{
        Host: "localhost", Port: 7233,
        Namespace: "default", TaskQueue: "my-app",
    }),
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(llmClient),
)
defer a.Close()

result, err := a.Run(ctx, "Hello", "")
// result.Content, result.AgentName, result.Model
```

[examples/simple_agent](examples/simple_agent)

### Temporal connection

Provide **either** `WithTemporalConfig` or `WithTemporalClient`, not both.

**Option 1 — WithTemporalConfig** (simple, local dev):

```go
agent.WithTemporalConfig(&agent.TemporalConfig{
    Host: "localhost", Port: 7233,
    Namespace: "default", TaskQueue: "my-app",
})
```

**Option 2 — WithTemporalClient** (TLS, API key auth, Temporal Cloud):

Use when you need mTLS, Temporal Cloud API keys, or other connection options. Create the client yourself and pass it. You must also call `WithTaskQueue`. The agent does not close the client; you own its lifecycle.

```go
import "go.temporal.io/sdk/client"

tc, _ := client.Dial(client.Options{
    HostPort:  "namespace-id.tmprl.cloud:7233",
    Namespace: "my-namespace",
    Credentials: client.NewAPIKeyStaticCredentials(apiKey),
    // Or: ConnectionOptions for mTLS, etc.
})
defer tc.Close()

a, _ := agent.NewAgent(
    agent.WithTemporalClient(tc),
    agent.WithTaskQueue("my-app"),
    agent.WithLLMClient(llmClient),
)
defer a.Close()
```

[examples/agent_with_temporal_client](examples/agent_with_temporal_client) demonstrates the full pattern.

### Create an LLM client (OpenAI, Anthropic, or Gemini)

```go
// OpenAI
llmClient, err := openai.NewClient(
    llm.WithAPIKey("sk-..."),
    llm.WithModel("gpt-4o"),
    llm.WithBaseURL("https://api.openai.com/v1"),  // optional
)

// Anthropic
llmClient, err := anthropic.NewClient(
    llm.WithAPIKey("..."),
    llm.WithModel("claude-3-5-sonnet-20241022"),
)

// Gemini
llmClient, err := gemini.NewClient(
    llm.WithAPIKey("..."),  // or GOOGLE_API_KEY
    llm.WithModel("gemini-2.5-flash"),
)
```

### Supported LLMs

| Provider      | Package             | Notes                     |
| ------------- | ------------------- | ------------------------- |
| **OpenAI**    | `pkg/llm/openai`    | GPT-4o, GPT-4o-mini, etc. |
| **Anthropic** | `pkg/llm/anthropic` | Claude models             |
| **Gemini**    | `pkg/llm/gemini`    | gemini-2.5-flash, etc.    |

You can add support for other LLM providers by implementing the `interfaces.LLMClient` interface in `[pkg/interfaces/llm.go](pkg/interfaces/llm.go)`. The interface requires:

- `Generate(ctx, *LLMRequest) (*LLMResponse, error)` — non-streaming completion
- `GenerateStream(ctx, *LLMRequest) (LLMStream, error)` — streaming completion
- `GetModel()`, `GetProvider()`, `IsStreamSupported()` — metadata

Implement `LLMStream` for streaming: `Next()`, `Current()`, `Err()`, `GetResult()`. See the existing providers in `pkg/llm/` for reference.

### Stream events (RunStream)

`RunStream` returns a channel of `AgentEvent`. Use `agent.WithStream(true)` for partial tokens as they arrive.

```go
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithStream(true),
)
defer a.Close()

eventCh, err := a.RunStream(ctx, "What's 17 * 23?", "")
for ev := range eventCh {
    switch ev.Type {
    case agent.AgentEventContentDelta:
        fmt.Print(ev.Content)
    case agent.AgentEventToolCall:
        fmt.Printf("tool: %s\n", ev.ToolCall.ToolName)
    case agent.AgentEventComplete:
        fmt.Println("done:", ev.Content)
    }
}
```

[examples/agent_with_stream](examples/agent_with_stream)

#### Displaying stream events

When streaming is enabled, the agent emits `ContentDelta` (partial tokens) and then `Complete` (full content). Both carry the same text—printing both would show it twice.

**Recommended pattern:** Track whether you already displayed content via `ContentDelta` or `Content`, and skip printing `Complete`'s content when so. Use `ev.Content` from `AgentEventComplete` as the canonical final result for programmatic use (e.g. logging, storage).

**Sub-agents:** You may see several `complete` events on one stream; it ends after the **main** assistant’s final `complete`. Use **`ev.AgentName`** to tell specialist from main when you print or log output.

```text
ContentDelta → "The result is 40."   (streamed, shown to user)
ContentDelta → ...
Complete     → "The result is 40."   (don't re-print; use ev.Content in code)
```

**Event types:** `ContentDelta` (streamed tokens), `Content` (full block when not streaming), `ToolCall`, `ToolResult`, `Approval` (human gate for a tool call or delegation; use `ev.Approval.Kind`), `Complete` (final response), `Error`.

See [examples/agent_with_stream_conversation](examples/agent_with_stream_conversation) for a full example: RunStream with conversation and the event-handling pattern.

### Tools

Register tools and pass to the agent. Use `agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy())` to skip approval (or omit for default approval flow).

```go
reg := tools.NewRegistry()
reg.Register(calculator.New())
reg.Register(weather.New())

a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithToolRegistry(reg),
    agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
)
defer a.Close()

result, _ := a.Run(ctx, "What's the weather in Tokyo?", "")
```

[examples/agent_with_tools](examples/agent_with_tools)

### Sub-agents

Build each specialist with `**NewAgent**` (own `**TaskQueue**`, LLM, tools, prompts). Register them on the main agent with `**WithSubAgents**`. Use `**WithName**` and `**WithDescription**` on specialists when you want clearer labels for the main agent’s model. Use `**WithMaxSubAgentDepth**` only if the default nesting limit is not enough. Run `**Run**`, `**RunStream**`, or `**RunAsync**` on the main agent. Sub-agents always run without a conversation ID—they do not inherit the main agent session history. If you use `**DisableWorker**`, pair each `**NewAgentWorker**` with the same options as the `**NewAgent**` that runs that agent.

For streaming scenarios, the main agent is the single subscription point. When using `RunStream`, events from all delegated sub-agents fan in to the same main-agent stream, including sub-agent tool approvals and tool call/result events.

```go
mathAgent, _ := agent.NewAgent(
    agent.WithName("MathSpecialist"),
    agent.WithDescription("Arithmetic; uses calculator tools."),
    agent.WithTemporalConfig(&agent.TemporalConfig{
        Host: "localhost", Port: 7233, Namespace: "default",
        TaskQueue: "my-app-math",
    }),
    agent.WithLLMClient(llmClient),
    agent.WithToolRegistry(mathTools),
    agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
)
defer mathAgent.Close()

mainAgent, _ := agent.NewAgent(
    agent.WithName("Main agent"),
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithTemporalConfig(&agent.TemporalConfig{
        Host: "localhost", Port: 7233, Namespace: "default",
        TaskQueue: "my-app-main-agent",
    }),
    agent.WithLLMClient(llmClient),
    agent.WithSubAgents(mathAgent),
    agent.WithMaxSubAgentDepth(2),
    agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
)
defer mainAgent.Close()

result, _ := mainAgent.Run(ctx, "What is 144 divided by 12?", "")
```

[examples/agent_with_subagents](examples/agent_with_subagents)

**RunStream event fan-in:** Subscribe once on the main agent and you receive events from the whole delegation tree, including sub-agent tool calls and approvals. Use **`ev.AgentName`** on each `AgentEvent` to see which agent produced the event (content, tools, approvals, complete). The approval payload is `ev.Approval` (`ApprovalEvent`); the requesting agent is **not** duplicated there—use `ev.AgentName`.

### Tool approval

By default tools require approval, including delegation to sub-agents registered with `**WithSubAgents`**—they follow the same `**WithToolApprovalPolicy**` as your other tools. Use `WithApprovalHandler` on the agent (required for Run when any tool needs approval). See [examples/agent_with_subagents](examples/agent_with_subagents).

#### Tools vs agent policy

- `**WithToolApprovalPolicy**` applies to **every** tool the agent exposes: registry / `WithTools` **and** sub-agent delegation. Default is require-all; `AutoToolApprovalPolicy()` skips all; `AllowlistToolApprovalPolicy("echo", "subagent_MathSpecialist", ...)` skips only those names.
- Custom tools may implement `**interfaces.ToolApproval`**; with a normal `**NewAgent**` build, a default policy is always set, so `**WithToolApprovalPolicy` wins** for that agent (see `requiresApproval` in `config.go`).

#### Sub-agents and approval

- `**ApprovalRequest`** (Run / RunAsync) and stream `**ev.Approval**` (`**ApprovalEvent**`) include `**Kind**` (`tool` or `delegation`) and `**DelegateToName**` (target specialist when `Kind` is `delegation`). The agent that asked for approval is on **`ev.AgentName`** for RunStream (and `req.AgentName` on `ApprovalRequest`).
- **Parent (main agent):** one policy for its whole list—e.g. `RequireAll` → approving `subagent_MathSpecialist` is the same flow as approving `calculator` on that agent. `AutoToolApprovalPolicy()` → no approval for delegation or other tools on that agent.
- **Specialist:** separate agent, **its own** `WithToolApprovalPolicy`. Calculator calls inside the specialist use **that** policy, not the parent’s.

```text
Main agent: WithToolApprovalPolicy(RequireAll)     → delegate to math → user approval
Math agent:  WithToolApprovalPolicy(Auto)         → calculator inside specialist → no approval
Math agent:  WithToolApprovalPolicy(RequireAll)   → calculator inside specialist → approval (fan-in on main stream)
```

Each `ApprovalRequest` includes `Respond`; call `req.Respond(Approved|Rejected)` when ready (same as RunAsync):

```go
a, _ := agent.NewAgent(
    agent.WithApprovalHandler(func(ctx context.Context, req *agent.ApprovalRequest) {
        // Prompt user, then:
        _ = req.Respond(agent.ApprovalStatusApproved) // or Rejected
    }),
    // ...
)
a.Run(ctx, prompt, "")
```

**RunStream** — receive `AgentEventApproval` and call `agent.OnApproval`:

```go
for ev := range eventCh {
    if ev.Type == agent.AgentEventApproval && ev.Approval != nil {
        // Show UI, then:
        a.OnApproval(ctx, ev.Approval.ApprovalToken, agent.ApprovalStatusApproved)
    }
}
```

**RunAsync** — channel-based completion without streaming. Do not set `WithApprovalHandler` for this path (it is replaced for the duration of the run). Receive each pending approval on `approvalCh` and call `req.Respond` (same idea as `WithApprovalHandler`):

```go
resultCh, approvalCh, err := a.RunAsync(ctx, prompt, "")
if err != nil { /* validation error before goroutine started */ }

go func() {
    for req := range approvalCh {
        _ = req.Respond(agent.ApprovalStatusApproved) // or Rejected
    }
}()

res := <-resultCh
if res.Err != nil { /* handle */ }
// res.Response.Content
```

For **Run** / **RunAsync**, use `req.Respond` only. For **RunStream**, use `OnApproval` as in the snippet above (first argument comes from `ev.Approval`).

[examples/agent_with_tools_approval](examples/agent_with_tools_approval)

[examples/agent_with_run_async](examples/agent_with_run_async)

**Approval timeout:** `WithApprovalTimeout` (default: `timeout − 30s`) limits how long the user has to approve or reject a tool. If they do not respond in time:

- **Run:** `Run()` returns `nil, err` with the failure.
- **RunStream:** An `AgentEventError` is emitted on the event channel with the error message.
- **RunAsync:** `resultCh` receives `RunAsyncResult` with `Err` set.

### Timeouts and deadlines

You can limit run duration in two ways:

**Option 1 — Context with deadline** (per-call):

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()
result, err := a.Run(ctx, "Hello", "")
```

**Option 2 — Agent `WithTimeout`** (when ctx has no deadline):

```go
a, _ := agent.NewAgent(
    agent.WithTimeout(5 * time.Minute),
    // ...
)
result, err := a.Run(context.Background(), "Hello", "")
```

**Notes:**

- ctx deadline always wins. If ctx has 2 min but agent has `WithTimeout(10 min)`, the run ends at 2 min.
- approvalTimeout (per-approval limit) comes from agent config. If ctx has 1 hour and you use neither option, approval still expires at ~4.5 min (default). Set `WithTimeout` or `WithApprovalTimeout` for longer approvals.

### Custom tools

Implement `interfaces.Tool`: `Name()`, `Description()`, `Parameters()`, `Execute()`. Register with `agent.WithTools(tool1, tool2)`.

[examples/agent_with_custom_tools](examples/agent_with_custom_tools)

### Response format

By default the agent uses **text-only** output. Use `agent.WithResponseFormat` to request structured output (e.g. JSON with a schema).

**Default (text):** No `WithResponseFormat` — the LLM responds as plain text.

```go
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    // No WithResponseFormat — text output
)
```

**JSON with schema:** Use `interfaces.ResponseFormatJSON` and a valid JSON Schema. The schema must have `type: "object"` at the root with `properties`:

```go
import "github.com/vvsynapse/agent-sdk-go/pkg/interfaces"

a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithResponseFormat(&interfaces.ResponseFormat{
        Type:   interfaces.ResponseFormatJSON,
        Name:   "AgentResponse",
        Schema: interfaces.JSONSchema{
            "type":       "object",
            "properties": interfaces.JSONSchema{
                "response": interfaces.JSONSchema{"type": "string"},
            },
            "required": []any{"response"},
        },
    }),
)
```

**Text explicitly:** Force plain text even if you later add other config:

```go
agent.WithResponseFormat(&interfaces.ResponseFormat{Type: interfaces.ResponseFormatText})
```

**Note:** Structured Outputs (JSON schema) require supported models (e.g. `gpt-4o`, `gpt-4o-mini`). Older models may use JSON mode instead. See your provider docs.

### Multiple agents

Use `agent.WithInstanceId` when multiple agents share a base TaskQueue:

```go
a1, _ := agent.NewAgent(
    agent.WithTemporalConfig(cfg),
    agent.WithInstanceId("agent-1"),
    ...
)
a2, _ := agent.NewAgent(
    agent.WithTemporalConfig(cfg),
    agent.WithInstanceId("agent-2"),
    ...
)
```

[examples/multiple_agents](examples/multiple_agents)

### Agent and worker in separate processes

Agent process: use `agent.DisableWorker()`. Worker process: use `agent.NewAgentWorker()` with the same config.

```go
// Worker process
w, _ := agent.NewAgentWorker(agent.WithTemporalConfig(...), agent.WithLLMClient(...))
defer w.Close()
go w.Start()

// Agent process
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.DisableWorker(),
)
result, _ := a.Run(ctx, "Hello", "")
```

[examples/agent_with_worker](examples/agent_with_worker)

### Conversation (message history)

Pass `agent.WithConversation(conv)` to persist message history for multi-turn context. Use `agent.WithConversationSize(n)` to limit how many messages are fetched for LLM context (default 20).

**Conversation ID:** When the agent is configured with a conversation, pass the same `conversationID` to both `Run(ctx, prompt, conversationID)` and `RunStream(ctx, prompt, conversationID)` for the same session—so history is shared across turns.

Choose implementation by deployment:

| Deployment                                                        | Use                                                       |
| ----------------------------------------------------------------- | --------------------------------------------------------- |
| **Single process** (agent and worker in same process)             | `inmem.NewInMemoryConversation`                           |
| **Remote workers** (`DisableWorker` or `WithEnableRemoteWorkers`) | `redis.NewRedisConversation` or another distributed store |

To add a new conversation store (e.g., Postgres, MongoDB), implement the `interfaces.Conversation` interface in `[pkg/interfaces/conversation.go](pkg/interfaces/conversation.go)`. The interface requires `AddMessage`, `ListMessages`, `Clear`, and `IsDistributed`. See `pkg/conversation/inmem` and `pkg/conversation/redis` for reference.

In-memory cannot be used with remote workers—the agent will return an error at build time.

**Remote workers:** Agent and worker must use the same conversation store (same Redis config) so both processes access the same data. Only the process that calls `Run` or `RunStream` passes the conversation ID; the worker does not.

```go
// Single process (default)
conv := inmem.NewInMemoryConversation(inmem.WithMaxSize(100))
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithConversation(conv),
    agent.WithConversationSize(20), // optional; default 20
)
result, _ := a.Run(ctx, "Hello", "session-1")

// Worker process
convW, _ := redis.NewRedisConversation(redis.WithAddr("localhost:6379"))
defer convW.Close()
w, _ := agent.NewAgentWorker(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithConversation(convW),
)
go w.Start()

// Agent process
convA, _ := redis.NewRedisConversation(redis.WithAddr("localhost:6379"))
defer convA.Close()
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.DisableWorker(),
    agent.WithConversation(convA),
)
result, _ := a.Run(ctx, "Hello", "session-1")
```

**Lifecycle:** You own the conversation. Call `Clear` when ending a session or when you no longer need the history. The agent never calls `Clear`.

**Example (in-memory, single process):**

```go
import (
    "github.com/vvsynapse/agent-sdk-go/pkg/agent"
    "github.com/vvsynapse/agent-sdk-go/pkg/conversation/inmem"
)

conv := inmem.NewInMemoryConversation(inmem.WithMaxSize(100))
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithConversation(conv),
    agent.WithConversationSize(20),
)
defer a.Close()

convID := "session-1"
a.Run(ctx, "I'm Alice. Remember that.", convID)
a.Run(ctx, "What's my name?", convID) // agent uses history: "Alice"
```

[examples/agent_with_conversation](examples/agent_with_conversation)

---

## Configuration

- **WithTemporalConfig**: Temporal connection (Host, Port, Namespace, TaskQueue). Use for simple setups. See [Temporal connection](#temporal-connection).
- **WithTemporalClient**: Pre-configured Temporal client. Use for TLS, API key auth, Temporal Cloud. Requires `WithTaskQueue`. Agent does not close the client.
- **WithTaskQueue**: Task queue name. Required when using `WithTemporalClient`. Ignored when using `WithTemporalConfig`.
- **WithResponseFormat**: LLM response format. Omit for text-only. Use `&interfaces.ResponseFormat{Type, Name, Schema}` for JSON with schema. See [Response format](#response-format).
- **WithConversation**: Message history store. Use `inmem` for single process; `redis` for remote workers. Pass same `conversationID` to `Run` and `RunStream` for a session. See [Conversation](#conversation-message-history).
- **WithConversationSize**: Max messages to fetch for LLM context (default 20). Only applies when `WithConversation` is set.
- **WithEnableRemoteWorkers**: Set `true` when using `DisableWorker` with approval or streaming.
- **WithSubAgents**: Attach specialist agents the main agent can delegate to. Each needs its own task queue and worker. See [Sub-agents](#sub-agents).
- **WithMaxSubAgentDepth**: Maximum delegation hops from this agent (default 2). See [Sub-agents](#sub-agents).
- **WithMaxIterations**: Max LLM rounds (default 5).
- **WithStream**: Enable `RunStream` partial content streaming.
- **WithLLMSampling**: Per-agent sampling (`Temperature`, `MaxTokens`, `TopP`, `TopK`). Pass `&agent.LLMSampling{...}`; nil fields = provider default. Extensible for more params.
- **WithApprovalTimeout**: Max wait per tool approval; must be less than agent timeout. Defaults to timeout−30s when tools require approval. Capped at 31 days.

**Env config:** [examples/README.md](examples/README.md) for examples; [cmd/README.md](cmd/README.md) for CLI.

---

## Development

Contributors: see **[CONTRIBUTING.md](CONTRIBUTING.md)** for prerequisites (Go version, Temporal setup), development workflow, and guidelines.
Project policies: **[SECURITY.md](SECURITY.md)** for vulnerability reporting and **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)** for community standards.

Quick commands: `make test` | `make lint` | `make tidy` | `make test-coverage`

---

## Setup and run examples

```bash
git clone <repo-url>
cd agent-sdk-go
cp examples/env.sample examples/.env
# Edit examples/.env: set LLM_APIKEY, LLM_MODEL
```

See **[examples/README.md](examples/README.md)** for how to run examples and the CLI.

### CLI configuration

The CLI uses a YAML config file. Copy the sample and add your values:

```bash
cp cmd/config.sample.yaml cmd/config.yaml
# Edit cmd/config.yaml: set llm.apiKey (or use AGENT_LLM_APIKEY env var)
go run ./cmd
```

Or run with a custom config path: `go run ./cmd -config /path/to/config.yaml`.

- **config.sample.yaml** — template in the repo (safe to commit)
- **config.yaml** — your config (gitignored; copy from sample)
- **Env overrides** — `AGENT_LLM_APIKEY`, `AGENT_TEMPORAL_HOST`, etc. override file values

See **[cmd/README.md](cmd/README.md)** for CLI details and env vars.

## Production Readiness Checklist

Before using this SDK in production, align with what it actually exposes and how agents run on Temporal:

- **Run and approval limits** — Use `WithTimeout` and/or a context deadline on `Run` / `RunStream`; use `WithApprovalTimeout` when tools require approval (activity retry counts inside workflows are fixed in the SDK, not user-tunable).
- **Bound agent loops** — Set `WithMaxIterations` and, if you use sub-agents, `WithMaxSubAgentDepth`.
- **Tool and delegation risk** — Choose `WithToolApprovalPolicy` per agent (main and specialists); use human review for dangerous tools and delegation where policy requires it.
- **Split processes** — If you use `DisableWorker` or `WithEnableRemoteWorkers`, use a distributed conversation store (e.g. Redis) and exercise approval/streaming paths in integration tests.
- **Secrets and data** — Keep LLM and Temporal credentials out of source control; treat tool arguments and model output as untrusted in your app.
- **LLM safety** — Validate and sanitize prompts, tool args, and model output at your integration boundary.
- **Operations** — Use your logger (`WithLogger` / `WithLogLevel`) and the Temporal UI/history for a given run; after upgrading this module, confirm workflows still replay in your environment.

---

## Disclaimer

This project is provided "as is" under the MIT License. When building AI agents that execute real-world actions, ensure appropriate safeguards, validation, and human-in-the-loop approval workflows are in place. You are responsible for compliance, access control, and operational safety in your deployment. For security issues, follow **[SECURITY.md](SECURITY.md)**.
