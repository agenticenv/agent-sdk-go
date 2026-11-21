# Action policy

Denies `shell`. Steps up when `refund` is labeled `restricted`. Other tool calls are allowed. Receipts are appended to `receipts.jsonl`, and the public key is written to `action-policy.pub`.

```bash
go run ./agent_with_action_policy
go run ./agent_with_action_policy "Refund order 17"
go run ./agent_with_action_policy verify receipts.jsonl
```

On Temporal, register the same policy id, version, agent id, and key id on the starter and the worker.
