# Agent with memory (`agent_with_memory`)

Examples that wire **long-term memory** into **agent-sdk-go**. Pick **one backend** per run.

| Backend | Package | Example entrypoint |
|---------|---------|-------------------|
| Weaviate | [`pkg/memory/weaviate`](../../pkg/memory/weaviate) | `go run ./agent_with_memory/weaviate` |
| PostgreSQL + pgvector | [`pkg/memory/pgvector`](../../pkg/memory/pgvector) | `go run ./agent_with_memory/pgvector` |

Store mode is selected with **`MEMORY_STORE_MODE`** (`always` or `ondemand`; default `ondemand`). Both backends support both modes. `task examples:local` runs four combinations (each backend × each mode).

Uses the same Docker stack as retriever examples ([`../docker/`](../docker/)). `task infra:weaviate:up` / `task infra:pgvector:up` creates the **memory** class/table (`AgentMemory` / `agent_memories`) in addition to retriever schema. No seed rows for memory — rows are written by agent runs.

## Prerequisites

- **Runtime** — **`AGENT_RUNTIME=local`** (default): in-process. Optional durable backends:
  - **`AGENT_RUNTIME=temporal`**: from `examples/`, run `task infra:temporal:up` (and `task infra:temporal:wait` if needed). See [`temporal-setup.md`](../../temporal-setup.md).
  - **`AGENT_RUNTIME=restate`**: from `examples/`, run `task infra:restate:up` (and `task infra:restate:wait` if needed). See [`restate-setup.md`](../../restate-setup.md).
- **`examples/.env`** — `LLM_APIKEY`, `LLM_MODEL`, and **`EMBEDDING_OPENAI_APIKEY`** (see **`.env.defaults`**)
- **Task** (`go-task`) and **Docker** (`task infra:weaviate:up` or `task infra:pgvector:up`)

From `examples/`:

```bash
task infra:status    # see what is up
```

## Example behavior

- **Store mode** — `MEMORY_STORE_MODE=always` extracts and stores at run end; `ondemand` registers `save_memory` for the LLM during the run (default).
- **No CLI args** — two runs in one process: run 1 stores a preference, run 2 recalls it.
- **With args** — single custom prompt.
- **Scope** — `MEMORY_USER_ID` in `.env` (default `demo-user`); must be the same across runs you want to share memories.

Set `MEMORY_RECALL_ENABLED=false` in `.env` for store-only (skip load before LLM).

Use `SHOW_TELEMETRY=true` to see `total_memory_stores` on run 1 when store succeeds.

---

## Weaviate

Weaviate embeds memory text via **nearText** (`text2vec-openai` in Docker).

### Setup

```bash
cd examples
task infra:weaviate:up
task infra:weaviate:down   # when finished
```

Compose: [`docker/docker-compose.yml`](../docker/docker-compose.yml). Seed: [`docker/weaviate/seed.sh`](../docker/weaviate/seed.sh) (creates class **`AgentMemory`**).

`EMBEDDING_OPENAI_APIKEY` must be set in `examples/.env` **before** `up`. After a key change: `task infra:weaviate:down && task infra:weaviate:up`.

Verify the memory class exists:

```bash
curl -s http://localhost:8081/v1/schema | jq '.classes[].class'
# expect Document and AgentMemory
```

### Environment

```bash
WEAVIATE_HTTP_PORT=8081
WEAVIATE_HOST=localhost:8081
WEAVIATE_SCHEME=http
WEAVIATE_MEMORY_CLASS=AgentMemory
MEMORY_USER_ID=demo-user
MEMORY_RECALL_ENABLED=true
MEMORY_RECALL_LIMIT=10
MEMORY_RECALL_MIN_SCORE=0.35
```

### Run

```bash
go run ./agent_with_memory/weaviate
go run ./agent_with_memory/weaviate "Remember my favorite color is blue"
```

```bash
SHOW_TELEMETRY=true go run ./agent_with_memory/weaviate
```

### Weaviate troubleshooting

| Symptom | What to do |
|---------|------------|
| `missing class data` / memory recall error on first run | Usually empty class — Weaviate returns `null` not `[]` (fixed in SDK). Update and re-run; or `MEMORY_RECALL_ENABLED=false` for store-only |
| Class **`AgentMemory`** missing from schema | `task infra:weaviate:down && task infra:weaviate:up`; verify: `curl -s http://localhost:8081/v1/schema \| jq '.classes[].class'` |
| Compose / API key errors | Set `EMBEDDING_OPENAI_APIKEY`, then `task infra:weaviate:down && task infra:weaviate:up` |
| Connection refused `:8081` | `task infra:status`, `curl -s http://localhost:8081/v1/.well-known/ready`, `docker logs weaviate` |
| Run 2 does not recall run 1 | Same `MEMORY_USER_ID`; ensure run 1 completed and run-end store succeeded (check `LOG_LEVEL=debug` or `SHOW_TELEMETRY=true`) |
| Port 8081 / 50051 in use | `task infra:weaviate:down`; set `WEAVIATE_HTTP_PORT` / `WEAVIATE_GRPC_PORT` (and matching `WEAVIATE_HOST`) before `up` |

```bash
LOG_LEVEL=debug go run ./agent_with_memory/weaviate
```

---

## pgvector

Client-side **OpenAI-compatible** embeddings, then cosine search in Postgres ([pgvector](https://github.com/pgvector/pgvector)).

### Setup

```bash
cd examples
task infra:pgvector:up
task infra:pgvector:down   # when finished
```

Schema: [`docker/pgvector/setup.sql`](../docker/pgvector/setup.sql) (table **`agent_memories`**). Seed: [`docker/pgvector/seed.sh`](../docker/pgvector/seed.sh).

Default DSN (in **`.env.defaults`**): `postgres://postgres:secret@localhost:5432/vectordb?sslmode=disable`

Verify the memory table exists:

```bash
docker exec pgvector psql -U postgres -d vectordb -c "\d agent_memories"
```

### Environment

```bash
PGVECTOR_DSN=postgres://postgres:secret@localhost:5432/vectordb?sslmode=disable
PGVECTOR_MEMORY_TABLE=agent_memories
EMBEDDING_OPENAI_MODEL=text-embedding-3-small
EMBEDDING_OPENAI_APIKEY=sk-...
MEMORY_USER_ID=demo-user
MEMORY_RECALL_ENABLED=true
MEMORY_RECALL_LIMIT=10
MEMORY_RECALL_MIN_SCORE=0.35
```

With **Anthropic/Gemini** chat, `EMBEDDING_OPENAI_APIKEY` is still required (not `LLM_APIKEY`).

### Run

```bash
go run ./agent_with_memory/pgvector
go run ./agent_with_memory/pgvector "What answer style do I prefer?"
```

```bash
SHOW_TELEMETRY=true go run ./agent_with_memory/pgvector
```

### pgvector troubleshooting

| Symptom | What to do |
|---------|------------|
| `relation "agent_memories" does not exist` | `task infra:pgvector:down && task infra:pgvector:up`; verify with `\d agent_memories` above |
| `embedding config` / Anthropic chat | Set `EMBEDDING_OPENAI_APIKEY`; re-run `task infra:pgvector:up` |
| `PGVECTOR_DSN is required` | Use default DSN or match compose `PGVECTOR_*` vars |
| Run 2 does not recall run 1 | Same `MEMORY_USER_ID`; ensure run 1 finished without error and the LLM called `save_memory` (check `LOG_LEVEL=debug` or `SHOW_TELEMETRY=true`) |
| Dimension / SQL errors | Model must match `vector(1536)` in `setup.sql` |
| Port 5432 in use | `task infra:pgvector:down`; set `PGVECTOR_PORT` and update `PGVECTOR_DSN` |

```bash
LOG_LEVEL=debug go run ./agent_with_memory/pgvector
```
