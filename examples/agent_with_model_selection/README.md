# agent_with_model_selection

Demonstrates **model selection** — a per-run client, or a function that picks a named client:

| Scenario | What this example does |
|---|---|
| This run | `LLMClient: "local"` on `Run` |
| Sensitive prompt | Per-run selector sends a prompt that contains "ssn" to `"local"` |
| Tool rounds | Per-call selector uses `"cheap"` until a tool result is in the request, then the primary client answers |

Uses stub LLM clients and a stub tool. No extra infrastructure. No provider key required.

## Run

From `examples/`:

```bash
go run ./agent_with_model_selection
```

## Durable runtimes

**Temporal:** Register the same `WithNamedLLMClients` and `WithLLMSelector` mode on the agent starter and the worker. The selector function body is not compared.

**Restate:** The selector runs in the process that serves the embedded SDK endpoint (`AGENT_RUNTIME=restate`).
