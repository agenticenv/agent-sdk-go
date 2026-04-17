# Temporal setup

A **running Temporal Service** is required for any application that uses the Temporal SDK: workflows and activities execute on the cluster. This page is a **Temporal** reference only — how to run a dev server locally and where to read official docs for production.

---

## Local development

For building and testing, run a **development server** on your machine. Temporal documents this as the usual path before you deploy anywhere else.

**Typical choices:**

| Approach | When it helps |
|----------|----------------|
| **Docker** | One command, predictable ports, matches many team setups. |
| **Temporal CLI** | Single binary, `temporal server start-dev`, good for quick iteration. |

For the dev server (flags, UI, running the CLI inside Docker): **[Run the development server](https://docs.temporal.io/cli/setup-cli#run-the-development-server)**.

### Docker (common for local dev)

```bash
docker run --rm -p 7233:7233 -p 8233:8233 temporalio/temporal:latest server start-dev --ip 0.0.0.0
```

- **Service:** `localhost:7233` (gRPC)
- **Web UI:** http://localhost:8233

Keep the process running while you work.

### Temporal CLI

**Install** the `temporal` binary for your OS first — methods differ (e.g. Homebrew on macOS, package managers or archives on Linux, installer on Windows, or Docker). Do **not** copy install commands from here; use Temporal’s current instructions: **[Set up the Temporal CLI](https://docs.temporal.io/cli/setup-cli)**.

After install, start a local dev server:

```bash
temporal server start-dev
```

Use `temporal server start-dev --help` for persistence, database file, and other options. Networking when the CLI runs inside Docker is covered under **[Run the development server](https://docs.temporal.io/cli/setup-cli#run-the-development-server)**.

---

## Production and long-lived environments

Local `start-dev` is for **development**, not production operations.

| Path | Documentation |
|------|----------------|
| **Temporal Cloud** (managed) | **[Production deployments](https://docs.temporal.io/production-deployment)** — evaluate and operate on Cloud. |
| **Self-hosted** | **[Self-hosted guide](https://docs.temporal.io/self-hosted-guide)** — planning, **[deployment](https://docs.temporal.io/self-hosted-guide/deployment)**, security, upgrades. |

---

## Connecting your worker and client

Point your Temporal **client** and **workers** at the same **host**, **port**, and **namespace** as the cluster you started. A default local dev server is usually reachable at `localhost:7233` with namespace `default`; use your Cloud or self-hosted endpoint and credentials when not using local dev.
