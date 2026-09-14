# durable_agent

Interactive durability labs for crash recovery and stream reconnect. Pick a backend and run that folder independently.

| Lab | Path | Topology | Infrastructure |
|-----|------|----------|-----------------|
| **Local** | [`local/`](local/) | **Single process** — durable-go journal on local disk, durable by default | None |
| **Temporal** | [`temporal/`](temporal/) | Separate **agent** + **worker** (`DisableLocalWorker` + `NewAgentWorker`) | Temporal server |
| **Restate** | [`restate/`](restate/) | **Single process** — embedded SDK endpoint | Restate server |

All three use the same public reconnect APIs (`GetAgentStream` + `WithOffset`, `GetAgentRun`). Scenario steps differ by topology — the Local lab needs no server at all, since local runtime durability is on by default.

See each lab’s README for setup and exercises. Repo setup: [temporal-setup.md](../../temporal-setup.md) · [restate-setup.md](../../restate-setup.md) (Local needs neither).
