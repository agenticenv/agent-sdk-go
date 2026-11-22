# Action policy

Denies `shell`. Steps up when `refund` is labeled `restricted` (prompts for approval). Other tool calls are allowed. Each run writes a fresh `receipts.jsonl` and `action-policy.pub`.

`verify` only checks an existing ledger — it does not run the agent.

```bash
go run ./agent_with_action_policy
go run ./agent_with_action_policy "Refund order 17"
go run ./agent_with_action_policy verify receipts.jsonl
```

On Temporal, register the same policy id, version, agent id, and key id on the starter and the worker.
