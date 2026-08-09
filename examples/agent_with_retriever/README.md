# Agent with retriever (`agent_with_retriever`)

Examples that wire a **vector retriever** into **agent-sdk-go**. Pick **one backend** per run.

| Backend | Package | Example entrypoint |
|---------|---------|-------------------|
| Weaviate | [`pkg/retriever/weaviate`](../../pkg/retriever/weaviate) | `go run ./agent_with_retriever/weaviate` |
| PostgreSQL + pgvector | [`pkg/retriever/pgvector`](../../pkg/retriever/pgvector) | `go run ./agent_with_retriever/pgvector` |

Sample KB JSON (edit per backend, then re-seed): [`docker/weaviate/sample-documents.json`](../docker/weaviate/sample-documents.json), [`docker/pgvector/sample-documents.json`](../docker/pgvector/sample-documents.json). Infra: [`../docker/`](../docker/) (compose + seed scripts).

## Prerequisites

- **Runtime** — **`AGENT_RUNTIME=local`** (default): in-process. Optional durable backends:
  - **`AGENT_RUNTIME=temporal`**: from `examples/`, run `task infra:temporal:up` (and `task infra:temporal:wait` if needed). Compose Temporal is on `localhost:7233`. See [`temporal-setup.md`](../../temporal-setup.md).
  - **`AGENT_RUNTIME=restate`**: from `examples/`, run `task infra:restate:up` (and `task infra:restate:wait` if needed). See [`restate-setup.md`](../../restate-setup.md). Restate ingress is **8080**; example Weaviate is published on host **8081** so both can run together.
- **`examples/.env`** — `LLM_APIKEY`, `LLM_MODEL`, and **`EMBEDDING_OPENAI_APIKEY`** (see **`.env.defaults`**)
- **Task** (`go-task`) and **Docker** for the vector store you use (`task infra:weaviate:up` or `task infra:pgvector:up`)

From `examples/`:

```bash
task infra:status    # see what is up
```

## Retriever modes

Set `RETRIEVER_MODE` in `.env` (default `agentic`):

| Mode | Behavior |
|------|----------|
| `agentic` | Retriever exposed as a tool; LLM decides when to search |
| `prefetch` | Search runs once before the first LLM call; context injected into system prompt |
| `hybrid` | Prefetch and retriever tools |

Prefetch/hybrid embed your **exact user message** — use concrete questions aligned with the sample KB (returns, shipping, warranty, etc.).

---

## Weaviate

Weaviate embeds queries via **nearText** (`text2vec-openai` in Docker). Chat LLM can differ from the embedding provider.

### Setup

```bash
cd examples
task infra:weaviate:up
task infra:weaviate:down   # when finished
```

Compose: [`docker/docker-compose.yml`](../docker/docker-compose.yml). Seed: [`docker/weaviate/seed.sh`](../docker/weaviate/seed.sh).

`EMBEDDING_OPENAI_APIKEY` must be set in `examples/.env` **before** `up` (baked into the container). After a key change: `task infra:weaviate:down && task infra:weaviate:up`.

### Environment

```bash
WEAVIATE_HTTP_PORT=8081
WEAVIATE_HOST=localhost:8081
WEAVIATE_SCHEME=http
WEAVIATE_CLASS=Document
WEAVIATE_RETRIEVER_NAME=weaviate-kb
RETRIEVER_MODE=agentic
# WEAVIATE_MIN_SCORE=0.5   # optional; SDK default 0.75
```

### Run

```bash
go run ./agent_with_retriever/weaviate "What is the return policy?"
RETRIEVER_MODE=prefetch go run ./agent_with_retriever/weaviate "What is the return policy?"
```

Add `SHOW_TELEMETRY=true` to see retriever search counts (total, failed, prefetch/agentic breakdown) printed after the run:

```bash
SHOW_TELEMETRY=true go run ./agent_with_retriever/weaviate "What is the return policy?"
```

### Weaviate troubleshooting

| Symptom | What to do |
|---------|------------|
| Compose / API key errors | Set `EMBEDDING_OPENAI_APIKEY`, then `task infra:weaviate:down && task infra:weaviate:up` |
| Connection refused `:8081` | `task infra:status`, `curl -s http://localhost:8081/v1/.well-known/ready`, `docker logs weaviate` |
| Empty search / no relevant docs | Re-seed with `task infra:weaviate:up`; check `WEAVIATE_CLASS=Document`; list objects: `curl -s "http://localhost:8081/v1/objects?class=Document&limit=5"`; try `RETRIEVER_MODE=prefetch` |
| Port 8081 / 50051 in use | `task infra:weaviate:down`; set `WEAVIATE_HTTP_PORT` / `WEAVIATE_GRPC_PORT` (and matching `WEAVIATE_HOST`) before `up` |
| LLM ignores KB (agentic) | Confirm objects exist; use prefetch mode |

```bash
LOG_LEVEL=debug go run ./agent_with_retriever/weaviate "What is the return policy?"
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

Schema: [`docker/pgvector/setup.sql`](../docker/pgvector/setup.sql). Seed: [`docker/pgvector/seed.sh`](../docker/pgvector/seed.sh).

Default DSN (in **`.env.defaults`**): `postgres://postgres:secret@localhost:5432/vectordb?sslmode=disable`

### Environment

```bash
PGVECTOR_DSN=postgres://postgres:secret@localhost:5432/vectordb?sslmode=disable
PGVECTOR_TABLE=documents
PGVECTOR_RETRIEVER_NAME=pgvector-kb
EMBEDDING_OPENAI_MODEL=text-embedding-3-small
EMBEDDING_OPENAI_APIKEY=sk-...
PGVECTOR_MIN_SCORE=0.35
RETRIEVER_MODE=agentic
```

With **Anthropic/Gemini** chat, `EMBEDDING_OPENAI_APIKEY` is still required for search (not `LLM_APIKEY`).

### Run

```bash
go run ./agent_with_retriever/pgvector "What is the return policy?"
RETRIEVER_MODE=prefetch go run ./agent_with_retriever/pgvector "What is the return policy?"
```

Add `SHOW_TELEMETRY=true` to see retriever search counts (total, failed, prefetch/agentic breakdown) printed after the run:

```bash
SHOW_TELEMETRY=true go run ./agent_with_retriever/pgvector "What is the return policy?"
```

### pgvector troubleshooting

| Symptom | What to do |
|---------|------------|
| `no relevant documents found` | `task infra:status`; row count: `docker exec pgvector psql -U postgres -d vectordb -t -c "SELECT COUNT(*) FROM documents;"`; re-seed or lower `PGVECTOR_MIN_SCORE` |
| `embedding config` / Anthropic chat | Set `EMBEDDING_OPENAI_APIKEY`; re-seed: `task infra:pgvector:down && task infra:pgvector:up` |
| `PGVECTOR_DSN is required` | Use default DSN or match compose `PGVECTOR_*` vars |
| Dimension / SQL errors | Model must match `vector(1536)` in `setup.sql`; re-seed after model change |
| Port 5432 in use | `task infra:pgvector:down`; set `PGVECTOR_PORT` and update `PGVECTOR_DSN` |

```bash
LOG_LEVEL=debug go run ./agent_with_retriever/pgvector "What is the return policy?"
```

Look for `pgvector search done` with `docs=0` vs embedding errors.
