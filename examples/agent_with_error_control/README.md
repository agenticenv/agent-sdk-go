# agent_with_error_control

Demonstrates **error control** (`WithErrorControl`) — not lifecycle [`WithHooks`](../agent_with_hooks/README.md):

| Scenario | What this example does |
|---|---|
| `OnLLMFailure` | Stub primary returns `*LLMError` `rate_limit`; hook swaps to named client `"cheap"` |
| `OnMaxIterationsExceeded` | `MaxIterations` is 1; hook grants one extra round so the run can finish |
| Circuit breaker | Stub tool always fails with the same args; the third call is skipped |

Uses stub LLM clients and a stub tool. No extra infrastructure. No provider key required.

Error-control activity is printed to **stderr** with an `[error-control]` prefix.

## Run

From `examples/`:

```bash
go run ./agent_with_error_control
```

## Durable runtimes

**Temporal:** Register the same `WithNamedLLMClients` and `WithErrorControl` on the agent starter and the worker. Fingerprinted: hook **slots** (not bodies), fallback name, named-client model/provider, breaker thresholds.

**Restate:** Hooks run in the process that serves the embedded SDK endpoint (`AGENT_RUNTIME=restate`).
