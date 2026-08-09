# Agent with observability (`agent_with_observability`)

Two separate programs live in this folder; pick one entry point per run (no `-mode` flag):

| Directory | Command | What it demonstrates |
|-----------|---------|---------------------|
| **`config/`** | `go run ./examples/agent_with_observability/config/` | [`WithObservabilityConfig`](../../pkg/agent/config.go): the SDK builds OTLP **traces**, **metrics**, and **logs** (SDK logger → slog → OTLP using shared default batching and export intervals). |
| **`objects/`** | `go run ./examples/agent_with_observability/objects/` | [`observability.NewTracer`](../../pkg/observability/tracer.go) / [`NewMetrics`](../../pkg/observability/metrics.go) / [`NewLogs`](../../pkg/observability/logs.go), then [`WithTracer`](../../pkg/agent/config.go) / [`WithMetrics`](../../pkg/agent/config.go) / [`WithLogs`](../../pkg/agent/config.go). With no [`WithLogger`](../../pkg/agent/config.go), the SDK bridges the default logger to that OTLP log client (same behavior as logs under [`WithObservabilityConfig`](../../pkg/agent/config.go)). You can still pass [`WithLogger`](../../pkg/agent/config.go)([`logger.DefaultLoggerWithOtelProvider`](../../pkg/logger/logger.go)(…)) if you need a custom slog setup while reusing the same `LoggerProvider`. |

Shared OTLP env parsing and base agent options are in [`setup/setup.go`](setup/setup.go).

Do not combine **`WithObservabilityConfig`** with injected **`WithTracer` / `WithMetrics` / `WithLogs`** on the same agent for the same OTLP signal: when observability config is present and the matching **`Disable*`** flag is false, **`buildAgentConfig`** always builds tracer, metrics, and logs from that struct and **replaces** any injected clients (warnings are logged if you set both). Use either **`config/`** (observability-driven) or **`objects/`** (manual clients), or set **`DisableTraces` / `DisableMetrics` / `DisableLogs`** on the observability config to keep your injections for that signal.

## Prerequisites

1. **LLM** — `LLM_PROVIDER`, `LLM_APIKEY`, `LLM_MODEL`, optional `LLM_BASEURL` per [`../.env.defaults`](../.env.defaults). Add secrets in **`examples/.env`**.

2. **Durable runtime** (optional):
   - **Temporal** — when **`AGENT_RUNTIME=temporal`** (`TEMPORAL_HOST`, `TEMPORAL_PORT`, `TEMPORAL_NAMESPACE`, task queue). See [`temporal-setup.md`](../../temporal-setup.md).
   - **Restate** — when **`AGENT_RUNTIME=restate`** (`RESTATE_*`). See [`restate-setup.md`](../../restate-setup.md).

3. **OTLP collector** — accepts OpenTelemetry Protocol on **gRPC** (default) or **HTTP/protobuf**. Use **host:port only** (no `http://` scheme). See the table below.

## OTLP environment variables

| Variable | Required | Meaning |
|----------|----------|---------|
| **`OTEL_EXPORTER_OTLP_ENDPOINT`** | yes | Host and port only, e.g. `localhost:4317` (gRPC) or `localhost:4318` (HTTP). |
| **`OTLP_PROTOCOL`** | no | `grpc` (default) or `http`. Must match how your collector listens. |
| **`OTLP_INSECURE`** | no | Set to `true` for plaintext (typical for local dev). |

Typical ports:

| `OTLP_PROTOCOL` | Port (convention) | Example |
|-------------------|-------------------|---------|
| `grpc` | **4317** | `localhost:4317` |
| `http` | **4318** | `localhost:4318` |

### Quick local collector

Run any OpenTelemetry Collector (or compatible agent) with an OTLP receiver on the port you choose. See the [Collector documentation](https://opentelemetry.io/docs/collector/). Newer Docker images usually need a YAML config mounted; use your team’s standard collector setup.

### Grafana `otel-lgtm` (traces, metrics, and logs)

For a single local stack (OpenTelemetry Collector, **Tempo**, **Prometheus**, **Loki**, **Grafana**), use [Grafana Docker LGTM](https://grafana.com/docs/opentelemetry/docker-lgtm). Publish Grafana on **3000** and OTLP **gRPC** on **4317** (matches this repo when using `OTLP_PROTOCOL=grpc`):

```bash
docker run -d -p 3000:3000 -p 4317:4317 grafana/otel-lgtm
```

Then:

1. Open [http://localhost:3000](http://localhost:3000) and sign in with **`admin` / `admin`** (default in upstream docs).
2. **Traces** — **Explore** → datasource **Tempo** → query by service or trace, with a time range that includes your run (call **`Agent.Close()`** so OTLP batches flush before exit). **`service.name`** follows the agent name (e.g. **`observability-example-agent`** from [`setup.BaseAgentOptions`](setup/setup.go)).
3. **Metrics** — **Explore** → datasource **Prometheus** → **Metrics** browser or label queries for the same time range.
4. **Logs** — **Explore** → datasource **Loki** → label filters (e.g. **`service_name`**) aligned with your OTLP resource, same time range.

Exports for either example (gRPC to the container):

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
export OTLP_PROTOCOL=grpc
export OTLP_INSECURE=true
```

The LGTM image also exposes OTLP **HTTP/protobuf** on **4318** as a common OpenTelemetry default; to use it, add **`-p 4318:4318`**, set **`OTLP_PROTOCOL=http`**, and **`OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318`** to match [`setup.MustParseOTLP`](setup/setup.go).

### Jaeger All-in-One (traces only)

If you only need traces and a lightweight UI, [Jaeger all-in-one](https://www.jaegertracing.io/docs/latest/getting-started/) listens for OTLP **gRPC** on **4317** and serves the UI on **16686**:

```bash
docker run -d -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one:latest
```

Then open [http://localhost:16686](http://localhost:16686), pick your service, and click **Find Traces**. Use the same **`OTLP_PROTOCOL=grpc`**, **`OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317`**, and **`OTLP_INSECURE=true`** exports as in the Grafana section above (only one stack should bind **4317** at a time).

For HTTP OTLP:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318
export OTLP_PROTOCOL=http
export OTLP_INSECURE=true
```

## Commands (from repository root)

Configure LLM + OTLP (and Temporal/Restate if selected — see above), then:

```bash
# WithObservabilityConfig
go run ./examples/agent_with_observability/config/

# WithTracer / WithMetrics / WithLogs (pre-built OTLP clients + slog bridge)
go run ./examples/agent_with_observability/objects/
```

Optional prompt as extra arguments (default prompt if you pass none):

```bash
go run ./examples/agent_with_observability/config/ "Say hello in one sentence"
go run ./examples/agent_with_observability/objects/ "Say hello in one sentence"
```

Build verify-only:

```bash
go build -o /dev/null ./examples/agent_with_observability/config/
go build -o /dev/null ./examples/agent_with_observability/objects/
```

## What to expect

- **Stdout** — Each binary prints which entry style it uses (`entry=config` or `entry=objects`), the user line, and the assistant reply when Temporal and LLM succeed.
- **Telemetry** — If the collector is listening on the endpoint and protocol you configured, **traces**, **metrics**, and **logs** (when enabled) should reach your backend. With **Grafana `otel-lgtm`**, use **Explore** (**Tempo** / **Prometheus** / **Loki**) as in the steps above. If nothing is listening, the agent run may still succeed while exporter errors appear depending on log level.

## Layout

```
agent_with_observability/
  README.md           ← this file
  setup/setup.go      ← shared OTLP env + base agent options
  config/main.go      ← WithObservabilityConfig
  objects/main.go     ← WithTracer / WithMetrics / WithLogs (default logger bridged when WithLogger omitted)
```

## Related code

- Example env loader: [`examples/config.go`](../config.go)
- Observability: [`pkg/observability`](../../pkg/observability/)
- Agent options: [`pkg/agent/config.go`](../../pkg/agent/config.go) (`ObservabilityConfig`, `WithTracer`, `WithMetrics`, `WithLogs`, `WithLogger`)
