# durable_agent

Interactive durability labs for crash recovery and stream reconnect. Pick a backend and run that folder independently.

| Lab | Path | Topology |
|-----|------|----------|
| **Temporal** | [`temporal/`](temporal/) | Separate **agent** + **worker** (`DisableLocalWorker` + `NewAgentWorker`) |
| **Restate** | [`restate/`](restate/) | **Single process** — embedded SDK endpoint |

Both use the same public reconnect APIs (`GetAgentStream` + `WithOffset`, `GetAgentRun`). Scenario steps differ by topology.

See each lab’s README for setup and exercises. Repo setup: [temporal-setup.md](../../temporal-setup.md) · [restate-setup.md](../../restate-setup.md).
