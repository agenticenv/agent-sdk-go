# AG-UI / CopilotKit agent streaming (minimal)

A tiny **Go HTTP server** streams agent events as **SSE** (`POST /agui`). A **Next.js** UI uses **CopilotKit** with the usual **`/api/copilotkit`** runtime route; that route forwards requests to the Go agent via **`@ag-ui/client` `HttpAgent`**.

CopilotKit expects its **runtime** handler, not a raw Go URL, in `runtimeUrl`—so the React app uses `runtimeUrl="/api/copilotkit"` and the Go server URL is set in `app/api/copilotkit/route.ts` (or `AGENT_URL`).

## Prereqs

- `examples/.env` with `LLM_*` set, same as other examples. Optional durable runtime: `AGENT_RUNTIME=temporal` (+ `TEMPORAL_*`) or `AGENT_RUNTIME=restate` (+ `RESTATE_*`). See [`temporal-setup.md`](../../temporal-setup.md) / [`restate-setup.md`](../../restate-setup.md).

## 1) Start the Go agent server

From the **agent-sdk-go** repo root (or this example’s parent with `go` module in path):

```bash
go run ./examples/agent_with_agui/server
```

Listens on **`:8787`** by default (override with `PORT=` — avoids conflicting with apps on 8080). Health: `GET http://localhost:8787/health`.  
Stream: `POST http://localhost:8787/agui` with JSON `{"prompt":"Hello"}` or `{"messages":[{"role":"user","content":"..."}]}`.

## 2) Start the UI

```bash
cd examples/agent_with_agui/ui
npm install
npm run dev
```

Open **http://localhost:3000** (Next default). The dev server runs the Copilot runtime at `/api/copilotkit`, which talks to the Go server at `http://127.0.0.1:8787/agui` by default.

To point at another host:

```bash
AGENT_URL=http://127.0.0.1:8787/agui npm run dev
```

## 3) Try without the UI

```bash
curl -N -X POST http://localhost:8787/agui \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"prompt":"What is 2+2?"}'
```

You should see `data: {...}` lines (AG-UI-style JSON from `event.ToJSON()`).

## Notes

- **`ui/node_modules`** and **`ui/.next`** are listed in the repo root `.gitignore` — run `npm install` in `ui/` after clone; do not commit those directories.
- Keep the **Go server** running before using the chat UI. Temporal / Restate are only needed when `AGENT_RUNTIME` is set to that backend.
- CopilotKit / `@ag-ui/client` versions may need to stay compatible; if the UI errors, check [CopilotKit AG-UI docs](https://docs.copilotkit.ai) and align package versions.
