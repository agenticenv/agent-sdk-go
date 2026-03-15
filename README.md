# temporal-agents-go

Temporal-native AI agent SDK for building agents with [Temporal](https://temporal.io).

## What is this?

**temporal-agents-go** lets you build AI agents that run on Temporal. The agent uses an LLM (OpenAI or Anthropic) to reason and optionally call tools. Workflows, activities, and tool execution are all orchestrated by Temporal—giving you durability, retries, and visibility out of the box.

## Capabilities

- **LLM integration** — OpenAI and Anthropic with tool/function calling
- **Streaming** — Partial content and thinking deltas via `RunStream`
- **Tools** — Built-in tools and custom tools via `interfaces.Tool`
- **Tool approval** — Optional approval flow before executing tools
- **Temporal-native** — Workflows, activities, durable execution, retries

## Getting started

Prerequisites: [Temporal](https://docs.temporal.io/self-hosted-guide) running, Go 1.21+.

### Create an agent and run

```go
import (
    "github.com/vinodvanja/temporal-agents-go/pkg/agent"
    "github.com/vinodvanja/temporal-agents-go/pkg/llm"
    "github.com/vinodvanja/temporal-agents-go/pkg/llm/openai"
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

result, err := a.Run(ctx, "Hello")
// result.Content, result.AgentName, result.Model
```

[examples/simple_agent](examples/simple_agent)

### Create an LLM client (OpenAI or Anthropic)

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
```

### Stream events (RunStream)

`RunStream` returns a channel of `AgentEvent`. Use `agent.WithStream(true)` for partial tokens as they arrive.

```go
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithStream(true),
)
defer a.Close()

eventCh, err := a.RunStream(ctx, "What's 17 * 23?")
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

result, _ := a.Run(ctx, "What's the weather in Tokyo?")
```

[examples/agent_with_tools](examples/agent_with_tools)

### Tool approval

By default tools require approval. Use `agent.WithApprovalHandler` to handle approvals:

```go
a, _ := agent.NewAgent(
    agent.WithTemporalConfig(...),
    agent.WithLLMClient(...),
    agent.WithToolRegistry(reg),
    agent.WithApprovalHandler(func(ctx context.Context, req *agent.ApprovalRequest, onApproval agent.ApprovalSender) {
        // Prompt user, then call onApproval(agent.ApprovalStatusApproved) or onApproval(agent.ApprovalStatusRejected)
    }),
)
```

[examples/agent_with_tools_approval](examples/agent_with_tools_approval)

### Custom tools

Implement `interfaces.Tool`: `Name()`, `Description()`, `Parameters()`, `Execute()`. Register with `agent.WithTools(tool1, tool2)`.

[examples/agent_with_custom_tools](examples/agent_with_custom_tools)

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
result, _ := a.Run(ctx, "Hello")
```

[examples/agent_with_worker](examples/agent_with_worker)

---

## Configuration

- **EnableRemoteWorkers:** Set `agent.WithEnableRemoteWorkers(true)` when using `DisableWorker` with approval or streaming.
- **Env config:** [examples/README.md](examples/README.md) for examples; [cmd/README.md](cmd/README.md) for CLI.

---

## Setup and run examples

```bash
git clone <repo-url>
cd temporal-agents-go
cp examples/env.sample examples/.env
# Edit examples/.env: set LLM_APIKEY, LLM_MODEL
```

See **[examples/README.md](examples/README.md)** for how to run examples and the CLI.
