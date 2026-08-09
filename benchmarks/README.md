# Massive Concurrency & Scale Benchmark

This directory contains a standalone performance utility for the Go Agent SDK. It runs real `pkg/agent` execution loops under configurable load—mock LLM and tools by default—so you can measure latency, memory, CPU, token counts, and success rate without external API keys.

Use it to stress-test orchestration behavior (multi-turn runs, tool batches, sub-agents, local / Temporal / Restate runtime) before pointing the same harness at real LLMs and tools.

---

## The Core Purpose

Most agent benchmarks focus on token throughput alone. Production workloads also depend on how well the **orchestration layer** scales: many concurrent runs, multi-turn tool loops, sub-agent delegation, durable Temporal/Restate runs, and stable memory use over hundreds of executions.

This benchmark exercises the SDK’s actual agent engine (`agent.NewAgent`, `Run`) with:

- Configurable run count and concurrency
- Mock or (later) real LLM + tool backends
- Optional sub-agent trees
- Local in-process runtime, Temporal (optional external workers), or Restate (embedded endpoint)
- Structured metrics and reports for comparison across config changes

---

## How It Works

Each **run** calls `agent.Run()` once on a shared root agent instance. The mock LLM follows a fixed multi-turn script:

1. **Turn 1** — returns tool calls for all registered tools (benchmark tools and sub-agent tools when configured).
2. **Turn 2** — returns a final text response after tool results are applied.

Mock components apply configurable latency and jitter so results reflect realistic timing, not instant stubs:

| Component | Behavior |
| :--- | :--- |
| **Mock LLM** | `Generate` with base latency + jitter; reports fixed token usage per call (`mock_tokens`, split into input/output). |
| **Mock tools** | `benchmark_tool_1` … `benchmark_tool_N` with base latency + jitter. |
| **Sub-agents** | Built as real SDK sub-agents (`subagent-1`, `subagent-1.1`, …); each runs the same mock script inside its own agent loop. |
| **Tool execution mode** | `sequential` or `parallel` maps to `agent.WithAgentToolExecutionMode`. |

**Concurrency:** one root agent is reused for all runs. When `concurrent: true`, runs execute in batches of `concurrent_count` (goroutines), each batch waiting for the previous batch to finish.

**Metrics collected per benchmark session:**

- Latency p50 / p95 / p99 / avg (wall-clock per `Run()`)
- Heap and total allocation delta
- Process CPU time
- Total input/output tokens (from mock LLM stats; includes sub-agent LLM calls)
- Success rate (`Run()` completed without error)
- Long-term memory recalls/stores (when `memory.enabled: true`; from run telemetry)
- `est_cost_usd` — placeholder `0` until pricing is configured

Reports are written to `benchmarks/reports/` (JSON or text). SDK logs (optional) go to `benchmarks/logs/`.

---

## Running the Benchmark

Run from the **repository root** (`agent-sdk-go/`):

### Quick run (default config)

Uses `benchmarks/config.yaml` (100 sequential runs, local runtime, 3 tools, 2 sub-agents):

```bash
go run ./benchmarks/
```

### Custom config file

```bash
go run ./benchmarks/ -config benchmarks/config.yaml
go run ./benchmarks/ -config /path/to/my-benchmark.yaml
```

### Command-line flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `-config` | `benchmarks/config.yaml` | Path to YAML config (searches `benchmarks/config.yaml` or `./config.yaml` if unset). |

All other settings are controlled via the YAML file. Edit `benchmarks/config.yaml` (or copy it) and re-run with `-config`.

### Example scenarios

**Fast local smoke test** — reduce runs and latency in a copy of the config:

```yaml
runtime: local
llm:
  latency_ms: 5
  jitter_ms: 0
tool:
  latency_ms: 2
  jitter_ms: 0
agent:
  runs: 10
  concurrent: false
  tools:
    count: 2
    execution: parallel
  subagents:
    count: 0
    levels: 0
```

```bash
go run ./benchmarks/ -config /tmp/fast-benchmark.yaml
```

**Concurrent batch runs:**

```yaml
agent:
  runs: 100
  concurrent: true
  concurrent_count: 10   # 10 runs in parallel per batch
```

**Temporal runtime** — requires a running Temporal server (`localhost:7233` by default):

```yaml
runtime: temporal
temporal:
  host: localhost
  port: 7233
  namespace: default
  task_queue: agent-sdk-go
  workers_count: 0   # embedded worker in agent process only
```

```bash
go run ./benchmarks/ -config benchmarks/config.yaml
```

**Restate runtime** — requires a running Restate server; embeds the SDK endpoint (`workers_count` must stay `0`):

```yaml
runtime: restate
restate:
  ingress_url: http://localhost:8080
  admin_url: http://localhost:9070
  endpoint_listen_address: ":9080"
```

**External root workers** (`workers_count: 1+`) — Temporal. Benchmark spawns separate worker processes that also poll the root agent's task queue. Embedded local workers still run for the root agent and all sub-agents (sub-agents always use embedded workers on their own task queues).

```yaml
runtime: temporal
temporal:
  workers_count: 2
```

Workers are started automatically and stopped when the benchmark finishes. You can also run a worker manually:

```bash
go run ./benchmarks/worker -config benchmarks/config.yaml -worker-id 1
```

**Debug logging** — SDK logs to timestamped files under `benchmarks/logs/`:

```yaml
logger:
  enabled: true
  dir: benchmarks/logs
  level: debug    # debug | info | warn | error
```

Log files: `agent_<timestamp>.log`, `worker_1_<timestamp>.log`, …

---

## Configuration reference

All paths in config (`dir` fields) are relative to the **repository root** unless absolute.

### `runtime`

| Value | Description |
| :--- | :--- |
| `local` | In-process SDK runtime (default). No durable server required. |
| `temporal` | Durable execution via Temporal. Server must be running before the benchmark. |
| `restate` | Durable execution via Restate. Server must be running; embedded SDK endpoint (no external workers). |

### `temporal`

| Field | Description |
| :--- | :--- |
| `host` | Temporal server host (default `localhost`). |
| `port` | gRPC port (default `7233`). |
| `namespace` | Temporal namespace (default `default`). |
| `task_queue` | Root agent task queue (default `agent-sdk-go`). Sub-agents use `{task_queue}-subagent-*` suffixes. |
| `workers_count` | `0` = embedded worker only. `1+` = spawn that many external root worker processes (Temporal). Used when `runtime: temporal`. |

### `restate`

| Field | Description |
| :--- | :--- |
| `ingress_url` | Restate ingress URL (default `http://localhost:8080`). |
| `admin_url` | Restate admin URL (default `http://localhost:9070`). |
| `endpoint_listen_address` | Embedded SDK endpoint listen address (default `:9080`). |
| `deployment_url` | Optional URL Restate uses to call back (Docker: `http://host.docker.internal:9080`). |
| `auth_key` | Optional ingress auth key. |

### `llm`

| Field | Description |
| :--- | :--- |
| `latency_ms` | Base delay per mock LLM `Generate` call. |
| `jitter_ms` | Random extra delay `[0, jitter_ms]` added on top of base latency. |
| `mock_tokens` | Total tokens reported per LLM call (split ~60% input / ~40% output). |

### `tool`

| Field | Description |
| :--- | :--- |
| `latency_ms` | Base delay per mock tool execution. |
| `jitter_ms` | Random extra delay `[0, jitter_ms]` on tool execution. |

### `agent`

| Field | Description |
| :--- | :--- |
| `runs` | Number of `Run()` calls on the root agent. |
| `concurrent` | `false` = runs one after another; `true` = batched parallel runs. |
| `concurrent_count` | Max parallel runs per batch when `concurrent: true`. |
| `tools.count` | Number of mock tools (`benchmark_tool_1` … `benchmark_tool_N`). |
| `tools.execution` | `sequential` or `parallel` — SDK tool batch execution mode. |
| `subagents.count` | Sub-agents per level (0 to disable). |
| `subagents.levels` | Max sub-agent nesting depth (1–5). |

### `memory`

Long-term memory (`agent.WithMemory`) using an in-process inmem backend (no Docker). Disabled by default.

| Field | Description |
| :--- | :--- |
| `enabled` | `true` wires recall before each run and store after (mode-dependent). |
| `store_mode` | `ondemand` (LLM `save_memory` tool) or `always` (extract at run end). |
| `user_id` | Scope user ID passed via `memory.WithContextUserID` (default `benchmark-user`). |

When `memory.enabled: true`, `agent.tools.count` may be `0` (memory-only runs). The mock LLM handles `save_memory` tool args and memory-extract JSON like the eval harness.

### `logger`

| Field | Description |
| :--- | :--- |
| `enabled` | `true` writes JSON SDK logs to files; `false` discards SDK logs. |
| `dir` | Log directory (default `benchmarks/logs`). |
| `level` | `debug`, `info`, `warn`, or `error`. |

### `output`

| Field | Description |
| :--- | :--- |
| `console` | Print report to stdout when `true`. |
| `file` | Write timestamped report file when `true`. |
| `dir` | Report directory (default `benchmarks/reports`). |
| `format` | `json` or `text`. |

### Sample output

#### Text (`output.format: text`)

```
=== Benchmark Report ===
Runtime          : local
Concurrent       : false
Total runs       : 100
Tools            : 3 (sequential)
Sub-agents       : 2 (levels 1)
---
Latency p50 (ms) : 245.00
Latency p95 (ms) : 312.00
Latency p99 (ms) : 389.00
Latency avg (ms) : 250.00
Heap alloc (B)   : 12345678
Total alloc (B)  : 98765432
CPU time (ms)    : 1500.00
Input tokens     : 50000
Output tokens    : 33333
Est. cost (USD)  : 0.0000  # pricing placeholder
Success rate (%) : 100.00
```

#### JSON (`output.format: json`)

Written to `benchmarks/reports/benchmark_<timestamp>.json`:

```json
{
  "runtime": "local",
  "generated_at": "2026-06-06T03:23:33Z",
  "config": {
    "Runtime": "local",
    "Temporal": {
      "Host": "localhost",
      "Port": 7233,
      "Namespace": "default",
      "TaskQueue": "agent-sdk-go",
      "WorkersCount": 0
    },
    "LLM": {
      "LatencyMs": 200,
      "JitterMs": 50,
      "MockTokens": 500
    },
    "Tool": {
      "LatencyMs": 50,
      "JitterMs": 10
    },
    "Agent": {
      "Runs": 100,
      "Concurrent": false,
      "ConcurrentCount": 10,
      "Tools": {
        "Count": 3,
        "Execution": "sequential"
      },
      "Subagents": {
        "Count": 2,
        "Levels": 1
      }
    },
    "Logger": {
      "Enabled": false,
      "Dir": "benchmarks/logs",
      "Level": "info"
    },
    "Output": {
      "Console": true,
      "File": true,
      "Dir": "benchmarks/reports",
      "Format": "json"
    }
  },
  "metrics": {
    "p50_ms": 245,
    "p95_ms": 312,
    "p99_ms": 389,
    "avg_ms": 250,
    "heap_alloc_bytes": 12345678,
    "total_alloc_bytes": 98765432,
    "cpu_time_ms": 1500,
    "total_input_tokens": 50000,
    "total_output_tokens": 33333,
    "est_cost_usd": 0,
    "total_runs": 100,
    "success_rate": 100
  }
}
```

---

## Note

LLM and tool calls are **mocked by default** with configurable latency and fixed token counts to keep results reproducible and free of API cost. Latency percentiles, memory, and CPU reflect real SDK orchestration overhead under that simulation.

When you swap in **real LLMs and tools**, metrics will change: latency follows network and model speed, token counts come from provider usage, and cost requires your own pricing model (the benchmark leaves `est_cost_usd` at `0` until configured). The harness structure—runs, concurrency, reporting, Temporal/Restate orchestration—stays the same; only the LLM client and tool registry need to be replaced in `benchmarks/setup/`.
