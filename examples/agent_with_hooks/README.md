# agent_with_hooks

Demonstrates **every** middleware hook point in the SDK with realistic transformations:

| Hook | What this example does |
|------|------------------------|
| `BeforeLLM` / `AfterLLM` | Redact email addresses and SSNs from prompts and responses |
| `BeforeTool` / `AfterTool` | Scrub PII from tool args and results |
| `BeforeRetrieve` / `AfterRetrieve` | Prefix queries with `kb:`; drop documents containing SSNs |
| `BeforeMemoryLoad` / `AfterMemoryLoad` | Require `tenant_id` in scope; wrap recalled context with a scrubbed header |
| `BeforeMemoryStore` / `AfterMemoryStore` | Scrub PII before persist; audit log after store |

Hook activity is printed to **stderr** with a `[hooks]` prefix so you can see when each hook fires without mixing into the assistant reply.

## Run

From `examples/`:

```bash
go run ./agent_with_hooks
```

Default: two-run demo (store memories + prefetch retrieval + tools, then recall).

```bash
go run ./agent_with_hooks "My email is alice@example.com. What is the return policy?"
```

## Durable runtimes

**Temporal:** Hooks are Go functions — they run in the **process that executes activities** (the worker). Register the same hook groups on both the agent starter and the worker via `HookOptions()` (or equivalent `WithHooks` calls). Group **names** are fingerprinted for drift detection; hook **logic** consistency is your responsibility.

**Restate:** Hooks run in the process that serves the embedded SDK endpoint (`AGENT_RUNTIME=restate`).
