# temporal-agents-go

Temporal-native AI agent SDK for building agents with [Temporal](https://temporal.io).

## What is this?

**temporal-agents-go** lets you build AI agents that run on Temporal. The agent uses an LLM (OpenAI or Anthropic) to reason and optionally call tools. Workflows, activities, and tool execution are all orchestrated by Temporal—giving you durability, retries, and visibility out of the box.

## Capabilities

- **LLM integration** — OpenAI and Anthropic with tool/function calling
- **Streaming** — Partial content and thinking deltas via `RunStream` (OpenAI, Anthropic)
- **Tools** — Built-in tools (calculator, weather, wikipedia, search) and custom tools via `interfaces.Tool`
- **Tool approval** — Optional approval flow before executing tools (policy: auto, allowlist, or require all)
- **Embedded worker** — Agent and worker run in the same process
- **Temporal-native** — Workflows, activities, durable execution, retries


### What you gain with Temporal

- **Durable execution** — The agent loop runs in a workflow; state is persisted. Worker crashes do not lose progress—Temporal replays and resumes automatically.
- **Automatic retries** — LLM calls, tool execution, and event publishing use configurable retry policies (exponential backoff, up to 3 attempts by default). Transient failures are retried without re-running the whole agent.
- **Visibility** — Runs are visible in Temporal UI; you can query workflow state, inspect history, and debug failures.
- **Long-lived approvals** — Tool approval can wait for human input across restarts; the workflow suspends until the approval activity completes.


## Getting started

### 1. Prerequisites

- [Temporal](https://docs.temporal.io/self-hosted-guide) running (or [Temporal Cloud](https://temporal.io/cloud))
- Go 1.21+

### 2. Setup

```bash
git clone <repo-url>
cd temporal-agents-go
cp env.sample .env
# Edit .env: set LLM_APIKEY, LLM_MODEL (gpt-4o or claude-3-5-sonnet-20241022)
```

### 3. Run an example

```bash
cd examples
go run ./simple_agent "Hello, what can you do?"
```

Or with tools:

```bash
go run ./agent_with_tools "What's the weather in Tokyo?"
```

Or with streaming (partial content as tokens arrive):

```bash
go run ./agent_with_stream "What's the current time and what's 17 * 23?"
```

Or interactive conversation (CLI loop until you type `exit`, `quit`, or `bye`):

```bash
go run ./cmd
```

## Examples

| Example | Description |
|---------|-------------|
| `simple_agent` | Minimal agent, no tools |
| `agent_with_tools` | Agent with built-in tools (echo, calculator, weather, wikipedia, search) |
| `agent_with_stream` | Streaming events via RunStream; partial content and thinking deltas |
| `agent_with_tools_approval` | Tools + user approval before each tool run |
| `agent_with_custom_tools` | Custom tools (reverser, word count) |
| `agent_with_worker` | Agent and worker in separate processes (not supported) |
| `multiple_agents` | Two agents in same process; sequential (default) or concurrent by arg |

```bash
cd examples
go run ./simple_agent
go run ./agent_with_tools "Roll a dice"
go run ./agent_with_stream "What's the current time?"
go run ./agent_with_tools_approval "What is 15 + 27?"
go run ./agent_with_custom_tools "Reverse 'hello world'"
go run ./multiple_agents "What is 7 times 8?"
go run ./multiple_agents concurrent "What is 7 times 8?"
```

## Usage

### Basic agent (OpenAI)

```go
import (
    "github.com/vinodvanja/temporal-agents-go/pkg/agent"
    "github.com/vinodvanja/temporal-agents-go/pkg/llm"
    "github.com/vinodvanja/temporal-agents-go/pkg/llm/openai"
)

a, _ := agent.NewAgent(
    agent.WithTemporalConfig(&agent.TemporalConfig{
        Host: "localhost", Port: 7233,
        Namespace: "default", TaskQueue: "my-app",
    }),
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(openai.NewClient(&llm.LLMConfig{
        APIKey: "sk-...",
        Model:  "gpt-4o",
    })),
)
defer a.Close()

result, err := a.Run(ctx, "Hello")
// result.Content, result.AgentName, result.Model
```

### Basic agent (Anthropic)

```go
import "github.com/vinodvanja/temporal-agents-go/pkg/llm/anthropic"

a, _ := agent.NewAgent(
    agent.WithTemporalConfig(&agent.TemporalConfig{...}),
    agent.WithSystemPrompt("You are a helpful assistant."),
    agent.WithLLMClient(anthropic.NewClient(&llm.LLMConfig{
        APIKey: "...",
        Model:  "claude-3-5-sonnet-20241022",
    })),
)
defer a.Close()
result, err := a.Run(ctx, "Hello")
```

### Streaming (RunStream)

`RunStream` returns a channel of `AgentEvent`—you receive events as the agent runs (content, tool calls, tool results, complete). This is the event-driven API.

`WithStream(true)` enables **partial content** streaming: tokens arrive as `content_delta` and `thinking_delta` events as they are generated, instead of waiting for the full `content` at the end. Requires an LLM that supports streaming (OpenAI, Anthropic). Default: `false`.

```go
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithStream(true),  // optional: partial content (tokens as they arrive)
)
defer a.Close()

eventCh, err := a.RunStream(ctx, "What's 17 * 23?")
if err != nil {
    log.Fatal(err)
}

for ev := range eventCh {
    if ev == nil {
        continue
    }
    switch ev.Type {
    case agent.AgentEventContentDelta:
        fmt.Print(ev.Content)  // partial token, no newline
    case agent.AgentEventThinkingDelta:
        fmt.Print(ev.Content)  // Anthropic extended thinking
    case agent.AgentEventContent:
        fmt.Println(ev.Content)  // full content
    case agent.AgentEventToolCall:
        fmt.Printf("tool: %s\n", ev.ToolCall.ToolName)
    case agent.AgentEventComplete:
        fmt.Println("done:", ev.Content)
    }
}
```

**Event types:** `content`, `tool_call`, `tool_result`, `error`, `complete` (always). With `WithStream(true)` you also get `content_delta` and `thinking_delta` (partial tokens).

You can combine `WithStream(true)` with tools and approval—see `examples/agent_with_stream`.

### Task queue

TaskQueue is required in TemporalConfig and must be unique per agent:

- **Single agent:** `TaskQueue: "my-agent"`
- **Multiple agents (same process):** Use different TaskQueues or `WithInstanceId()`:
  ```go
  // Option A: Different TaskQueue per agent
  agent.NewAgent(..., agent.WithTemporalConfig(&agent.TemporalConfig{..., TaskQueue: "my-agent-math"}))
  agent.NewAgent(..., agent.WithTemporalConfig(&agent.TemporalConfig{..., TaskQueue: "my-agent-creative"}))

  // Option B: Same base TaskQueue + WithInstanceId
  cfg := &agent.TemporalConfig{..., TaskQueue: "my-agent"}
  agent.NewAgent(..., agent.WithTemporalConfig(cfg), agent.WithInstanceId("agent-1"))
  agent.NewAgent(..., agent.WithTemporalConfig(cfg), agent.WithInstanceId("agent-2"))
  ```
- **Multiple instances (e.g. scaled pods):** `WithInstanceId(os.Getenv("POD_NAME"))` — each instance gets `{TaskQueue}-{instanceId}`
- **DisableWorker:** Not supported (use embedded worker)

### With tools

```go
import (
    "github.com/vinodvanja/temporal-agents-go/pkg/tools"
    "github.com/vinodvanja/temporal-agents-go/pkg/tools/calculator"
    "github.com/vinodvanja/temporal-agents-go/pkg/tools/echo"
)

reg := tools.NewRegistry()
reg.Register(echo.New())
reg.Register(calculator.New())

a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithToolRegistry(reg),
    agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()), // skip approval for demos
)
```

### Custom tools

Implement `interfaces.Tool`: `Name()`, `Description()`, `Parameters()`, `Execute()`.

```go
func (t *Reverser) Parameters() interfaces.JSONSchema {
    return tools.Params(
        map[string]interfaces.JSONSchema{
            "text": tools.ParamString("Text to reverse"),
        },
        "text",
    )
}
```

**Built-in tools:** echo, currenttime, random (demo), calculator, weather (Open-Meteo), wikipedia, search (Serper; needs `SERPER_API_KEY`).

### Tool approval

By default, all tools require approval. Use `WithToolApprovalPolicy` to relax:

```go
agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),           // no approval
agent.WithToolApprovalPolicy(agent.AllowlistToolApprovalPolicy("echo")), // only echo auto
agent.WithApprovalHandler(func(ctx context.Context, req *agent.ApprovalRequest, onApproved, onRejected agent.ApprovalSender) {
    if userSaysYes(req) {
        onApproved("")
    } else {
        onRejected("rejected")
    }
}),
```

## Configuration

Config via env (and `.env`). Copy `env.sample` to `.env`.

| Env var | Description |
|---------|-------------|
| `TEMPORAL_HOST`, `TEMPORAL_PORT`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASKQUEUE` | Temporal connection (TaskQueue required, unique per agent) |
| `LLM_TYPE` | `openai` or `anthropic` |
| `LLM_APIKEY` | API key |
| `LLM_MODEL` | e.g. `gpt-4o`, `claude-3-5-sonnet-20241022` |
| `LLM_BASEURL` | Optional (custom/proxy endpoints) |
| `SERPER_API_KEY` | For search tool |

CLI: `go run ./cmd` or `go run ./cmd -config cmd/config.yaml`.
