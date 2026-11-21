# Agent with guardrails

Checks the user prompt and the model response before the rest of the run uses them.

- `RegexRedact` replaces US Social Security numbers on input and output.
- `MaxLength` denies a prompt longer than 4000 characters (counted in Unicode code points).
- `moderate` denies a prompt that contains `MODERATION_BLOCK`. Replace that function with your own client.

```bash
cd examples
go run ./agent_with_guardrails
go run ./agent_with_guardrails "My SSN is 123-45-6789. What is the return policy?"
```

A denied run returns `agent.ErrGuardrailBlocked` and does not call the model.

**Temporal:** register the same guardrails, in the same order, on the starter and the worker.

**Restate:** guardrails run in the process that serves the embedded SDK endpoint (`AGENT_RUNTIME=restate`).
