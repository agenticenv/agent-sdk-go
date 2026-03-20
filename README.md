# temporal-agent-sdk-go

Temporal-native AI agent SDK for building agents with [Temporal](https://temporal.io).

## What is this?

**temporal-agent-sdk-go** lets you build AI agents that run on Temporal. The agent uses an LLM (OpenAI, Anthropic, or Gemini) to reason and optionally call tools. Temporal handles orchestration—giving you durability, retries, and visibility.

## Capabilities

- **LLM integration** — OpenAI, Anthropic, and Gemini with tool/function calling
- **Streaming** — Partial content and thinking deltas via `RunStream`
- **Tools** — Built-in tools and custom tools via `interfaces.Tool`
- **Tool approval** — Optional approval flow before executing tools
- **Temporal-native** — Durable execution, retries, visibility

## Getting started

Prerequisites: [Temporal](https://docs.temporal.io/self-hosted-guide) running, Go 1.21+.

### Create an agent and run

```go
import (
    "github.com/vvsynapse/temporal-agent-sdk-go/pkg/agent"
    "github.com/vvsynapse/temporal-agent-sdk-go/pkg/llm"
    "github.com/vvsynapse/temporal-agent-sdk-go/pkg/llm/openai"
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

```
ContentDelta → "The result is 40."   (streamed, shown to user)
ContentDelta → ...
Complete     → "The result is 40."   (don't re-print; use ev.Content in code)
```

**Event types:** `ContentDelta` (streamed tokens), `Content` (full block when not streaming), `ToolCall`, `ToolResult`, `Complete` (final response), `Error`.

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

### Tool approval

By default tools require approval. Use `WithApprovalHandler` on the agent (required for Run). Each `ApprovalRequest` includes `Respond`; call `req.Respond(Approved|Rejected)` when ready (same as RunAsync):

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

**RunStream** — receive `AgentEventToolApproval` and call `agent.OnApproval`:

```go
for ev := range eventCh {
    if ev.Type == agent.AgentEventToolApproval && ev.Approval != nil {
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
import "github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"

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
result, _ := a.Run(ctx, "Hello")
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
    "github.com/vvsynapse/temporal-agent-sdk-go/pkg/agent"
    "github.com/vvsynapse/temporal-agent-sdk-go/pkg/conversation/inmem"
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


| Option                      | Description                                                                                                                                                                                              |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **WithTemporalConfig**      | Temporal connection (Host, Port, Namespace, TaskQueue). Use for simple setups. See [Temporal connection](#temporal-connection).                                                                          |
| **WithTemporalClient**      | Pre-configured Temporal client. Use for TLS, API key auth, Temporal Cloud. Requires `WithTaskQueue`. Agent does not close the client.                                                                    |
| **WithTaskQueue**           | Task queue name. Required when using `WithTemporalClient`. Ignored when using `WithTemporalConfig`.                                                                                                      |
| **WithResponseFormat**      | LLM response format. Omit for text-only. Use `&interfaces.ResponseFormat{Type, Name, Schema}` for JSON with schema. See [Response format](#response-format).                                             |
| **WithConversation**        | Message history store. Use `inmem` for single process; `redis` for remote workers. Pass same `conversationID` to `Run` and `RunStream` for a session. See [Conversation](#conversation-message-history). |
| **WithConversationSize**    | Max messages to fetch for LLM context (default 20). Only applies when `WithConversation` is set.                                                                                                         |
| **WithEnableRemoteWorkers** | Set `true` when using `DisableWorker` with approval or streaming.                                                                                                                                        |
| **WithMaxIterations**       | Max LLM rounds (default 5).                                                                                                                                                                              |
| **WithStream**              | Enable `RunStream` partial content streaming.                                                                                                                                                            |
| **WithLLMSampling**         | Per-agent sampling (`Temperature`, `MaxTokens`, `TopP`, `TopK`). Pass `&agent.LLMSampling{...}`; nil fields = provider default. Extensible for more params.                                              |
| **WithApprovalTimeout**     | Max wait per tool approval; must be less than agent timeout. Defaults to timeout−30s when tools require approval. Capped at 31 days.                                                                     |


**Env config:** [examples/README.md](examples/README.md) for examples; [cmd/README.md](cmd/README.md) for CLI.

---

## Setup and run examples

```bash
git clone <repo-url>
cd temporal-agent-sdk-go
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