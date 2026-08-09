# Restate setup

A **running Restate server** is required for any application that uses the Restate agent runtime: the SDK submits runs through Restate ingress and serves handlers on an embedded endpoint that Restate calls back into. This page is a **Restate** reference only — how to run a server locally and where to read official docs for production.

---

## Local development

For building and testing, run a **development server** on your machine. Restate documents this as the usual path before you deploy anywhere else.

**Typical choices:**

| Approach | When it helps |
|----------|----------------|
| **Docker** | One command, predictable ports, matches many team setups. |
| **Restate binary** (`restate-server`) | Single binary, good for quick iteration without Compose. |
| **Examples Compose** | From `examples/`: `task infra:restate:up` — same image/ports as the Docker row below. |

For install options (Homebrew, download archives, Docker): **[Install Restate](https://docs.restate.dev/installation)**.

### Docker (common for local dev)

```bash
docker run --name restate_dev --rm \
  -p 8080:8080 -p 9070:9070 -p 9071:9071 \
  --add-host=host.docker.internal:host-gateway \
  docker.restate.dev/restatedev/restate:latest
```

- **Ingress:** `http://localhost:8080`
- **Admin / UI:** http://localhost:9070

Keep the process running while you work.

When Restate runs in Docker and your agent process runs on the **host**, set `RESTATE_DEPLOYMENT_URL` (or `Endpoint.DeploymentURL`) so Restate can call back into the SDK endpoint — typically `http://host.docker.internal:9080`. The examples compose service and `task infra:restate:up` use the same ports; see [examples/README.md](examples/README.md).

### Restate binary (`restate-server`)

**Install** the `restate-server` (and optional `restate` CLI) for your OS first — methods differ (e.g. Homebrew on macOS, download archives, or Docker). Do **not** copy install commands from here; use Restate’s current instructions: **[Install Restate](https://docs.restate.dev/installation)**.

After install, start a local server:

```bash
restate-server
```

For local admin registration of host endpoints, Restate docs often recommend advertising a local host (e.g. `RESTATE_ADVERTISED_HOST=127.0.0.1 restate-server`). Use `restate-server --help` for further options.

---

## Production and long-lived environments

Local Docker / `restate-server` is for **development**, not production operations.

| Path | Documentation |
|------|----------------|
| **Restate Cloud** (managed) | **[Restate Cloud](https://docs.restate.dev/cloud)** — managed Restate; use ingress auth (`AuthKey`) and request identity as required. |
| **Self-hosted** | **[Server deployment](https://docs.restate.dev/server/deploy/docker)** and related server docs — planning, persistence, and operations. |

---

## Connecting your agent

Point the SDK at the same **ingress** and (for auto-registration) **admin** URLs as the server you started:

| Setting | Local default | Purpose |
|---------|---------------|---------|
| `Ingress.URL` / `RESTATE_INGRESS_URL` | `http://localhost:8080` | Submit Run / Stream / Cancel; resolve awakeables |
| `Endpoint.AdminURL` / `RESTATE_ADMIN_URL` | `http://localhost:9070` | Optional — register this process’s SDK endpoint after listen |
| `Endpoint.ListenAddress` / `RESTATE_ENDPOINT_LISTEN_ADDRESS` | `:9080` | Where this process serves `AgentLoop` |
| `Endpoint.DeploymentURL` / `RESTATE_DEPLOYMENT_URL` | (empty → `http://127.0.0.1:9080`) | URL Restate uses to call back; override for Docker networking |
| `Ingress.AuthKey` / `RESTATE_AUTH_KEY` | (empty) | Bearer token for authenticated ingress (e.g. Restate Cloud) |

Pass these via [`restate.WithRestateConfig`](https://pkg.go.dev/github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate) — see the [Restate runtime](https://docs.agenticenv.ai/runtimes/restate) docs.
