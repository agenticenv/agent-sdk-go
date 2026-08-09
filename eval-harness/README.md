# Eval harness

Runs a single agent execution with mock LLM and mock tools. Prints JSON to stdout with `content`, `llm_usage`, and `telemetry` for evaluation assertions.

## Runner

From the repo root:

```bash
go run ./eval-harness/runner
go run ./eval-harness/runner -prompt "custom prompt"
go run ./eval-harness/runner -runtime temporal
go run ./eval-harness/runner -runtime restate
go run ./eval-harness/runner -tools 2
go run ./eval-harness/runner -config eval-harness/runner/config.yaml
```

### Arguments

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `eval-harness/runner/config.yaml` | Path to config file |
| `-prompt` | from config | Override `user_prompt` |
| `-runtime` | from config | Override `runtime` (`local`, `temporal`, or `restate`) |
| `-tools` | from config | Override `agent.tool_count` |

### config.yaml

Default path: `eval-harness/runner/config.yaml`

| Field | Default | Description |
|-------|---------|-------------|
| `runtime` | `local` | `local`, `temporal`, or `restate` |
| `user_prompt` | — | User message (required) |
| `agent.name` | `eval-agent` | Agent name |
| `agent.system_prompt` | built-in eval prompt | System instructions |
| `agent.tool_count` | `3` | Number of mock tools |
| `temporal.host` | `localhost` | Temporal host when `runtime: temporal` |
| `temporal.port` | `7233` | Temporal port |
| `temporal.namespace` | `default` | Temporal namespace |
| `temporal.task_queue` | `eval-harness` | Task queue |
| `restate.ingress_url` | `http://localhost:8080` | Restate ingress when `runtime: restate` |
| `restate.admin_url` | `http://localhost:9070` | Restate admin URL |
| `restate.endpoint_listen_address` | `:9080` | Embedded SDK endpoint listen address |
| `restate.deployment_url` | — | Optional callback URL (Docker) |
| `restate.auth_key` | — | Optional ingress auth key |

**Memory** (same file; `enabled: false` for default tool tests):

| Field | Default | Description |
|-------|---------|-------------|
| `memory.enabled` | `false` | Set true in YAML, or pass `-memory` on the runner |
| `memory.store_mode` | `ondemand` | `ondemand` or `always` |
| `memory.scenario` | `store_recall` | Two-run store then recall when enabled |
| `memory.store_prompt` / `memory.recall_prompt` | — | Used when scenario is active |

```bash
./eval-harness/run_agent_memory.sh ondemand   # same config.yaml, -memory flag
./eval-harness/run_agent_memory.sh always
```

Temporal mode uses an embedded local worker. Restate mode embeds the SDK endpoint; requires a running Restate server.

### Output

Stdout is always JSON:

```json
{
  "content": "eval complete",
  "llm_usage": { "prompt_tokens": 600, "completion_tokens": 400, "total_tokens": 1000 },
  "telemetry": { "run": { ... }, "tools": { ... }, "storage": { ... } },
  "memory_scenario": {
    "store": { "content": "...", "telemetry": { ... } },
    "recall": { "content": "...", "telemetry": { ... } }
  }
}
```

`memory_scenario` is present only for `memory.scenario: store_recall`.

## PromptFoo

Config: `eval-harness/promptfoo/config.yaml`

PromptFoo runs the eval harness as an [exec provider](https://www.promptfoo.dev/docs/providers/custom-script/). Each test invokes the runner once, parses the JSON stdout, and asserts on `content`, `llm_usage`, and `telemetry`.

### Run

```bash
cd eval-harness/promptfoo
npx promptfoo eval -c config.yaml
```

View results in the web UI:

```bash
npx promptfoo view
```

Requires Node.js. PromptFoo is installed on demand via `npx`; no local install is required.

### How it works

| Piece | Role |
|-------|------|
| **Provider** | `exec:../run_agent.sh` — shared wrapper in `eval-harness/` |
| **Prompt** | `"run eval check"` — passed as the first arg to `run_agent.sh` (overrides `user_prompt`) |
| **Output** | Runner JSON on stdout; assertions use `JSON.parse(output)` |
| **Paths** | `eval-harness/run_agent.sh` resolves repo root and runner config |

The runner accepts PromptFoo’s prompt as a positional argument when `-prompt` is not set. Agent settings (`tool_count`, `runtime`, etc.) still come from `eval-harness/runner/config.yaml`.

### Tests

Six test cases in `config.yaml` (each scoped to one provider — Promptfoo reports **6 runs**, not test×provider duplicates):

| Test | Provider | Checks |
|------|----------|--------|
| all mock tools were called | `eval-agent` | `telemetry.tools.breakdown` — three eval tools, each once |
| agent completed successfully | `eval-agent` | `finish_reason === "complete"` and `content === "eval complete"` |
| no failed tool calls | `eval-agent` | `telemetry.tools.failed_calls === 0` |
| llm usage reported | `eval-agent` | `llm_usage.total_tokens > 0` |
| memory ondemand stores then recalls | `eval-agent-memory-ondemand` | `memory_scenario` store/recall telemetry |
| memory always stores then recalls | `eval-agent-memory-always` | same for always store mode |

### Customizing

- **Change the prompt** — edit `prompts` in `promptfoo/config.yaml`, or add `vars` and use `{{var}}` in the prompt string.
- **Change agent behavior** — edit `eval-harness/runner/config.yaml` (tool count, runtime, system prompt), or adjust `eval-harness/run_agent.sh`.
- **Add tests** — append cases under `tests:` with `type: javascript` and `value:` returning a boolean.
- **Filter providers** — set `providers: [label]` on each test so agent checks do not run on memory providers (and vice versa).

## DeepEval

Python tests in `eval-harness/deepeval/`. The suite runs the Go eval harness, parses the JSON stdout, and asserts on `content`, `llm_usage`, and `telemetry` — the same output contract as the runner and PromptFoo.

### Run

```bash
cd eval-harness/deepeval
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
pytest test_agent.py -v
```

Requires Python 3.10+ and Go. No API key is required for the default tests.

### How it works

1. `harness.run_agent()` calls `eval-harness/run_agent.sh` and parses JSON.
2. Tests read telemetry from the agent SDK run output.
3. `assert_test()` runs DeepEval metrics where useful; plain pytest asserts cover the rest.

| Source field | Used for |
|--------------|----------|
| `content` | Agent response text |
| `llm_usage.total_tokens` | Token usage reported |
| `telemetry.run.finish_reason` | Run completed (`"complete"`) |
| `telemetry.tools.failed_calls` | No tool failures |
| `telemetry.tools.total_calls` | Expected call count |
| `telemetry.tools.breakdown` | Per-tool call counts; fed into `tools_called` for `ToolCorrectnessMetric` |

Example — extract tools from telemetry:

```python
agent_res = run_agent()
tools = list(agent_res["telemetry"]["tools"]["breakdown"].keys())
finish_reason = agent_res["telemetry"]["run"]["finish_reason"]
```

### Tests

Four pytest tests in `test_agent.py`:

| Test | Checks |
|------|--------|
| `test_agent_completes_with_telemetry` | `content`, `llm_usage`, `finish_reason`, `failed_calls`, `total_calls`, `breakdown` keys |
| `test_agent_tool_correctness` | `ToolCorrectnessMetric` — `tools_called` from telemetry vs expected tools |
| `test_memory_store_recall_ondemand` | `memory_scenario` store/recall telemetry (ondemand) |
| `test_memory_store_recall_always` | `memory_scenario` store/recall telemetry (always) |

### Customizing

- **Change the prompt** — pass a different string to `run_agent(prompt=...)`.
- **Change agent behavior** — edit `eval-harness/runner/config.yaml` or `eval-harness/run_agent.sh`.
- **Add tests** — extend `test_agent.py` with more telemetry asserts or DeepEval `LLMTestCase` fields.

> **Note:** CI runs both PromptFoo and DeepEval on PRs — see `.github/workflows/ci.yml` (`eval-harness` job). Locally: `task eval-harness` from the repo root.

