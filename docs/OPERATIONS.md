# Operations

This guide covers session storage, telemetry, and the CLI surfaces used to inspect
runtime behavior.

## Sessions

Each session gets its own folder under `.apex/sessions/<session-id>/`:

- `session.json` for resumable working-set state and session metadata
- `telemetry.json` for timestamped LLM and tool telemetry
- `workflows/` for coder-mode workflow JSON files created in that session

Resume flows:

- `apex -resume latest`
- `apex -resume <id>`
- `/resume` inside the TUI

You can also list sessions with `apex sessions` or `/sessions`, and start fresh
with `/new`.

## Telemetry

Session telemetry is file-based and structured for later analysis layers.

It records:

- prompt, completion, and total tokens
- cache creation and cache read tokens when reported by the provider
- per-call latency
- exact provider-bound `input_messages`
- exact provider `output_message`
- raw tool-call arguments
- tool results
- workflow, task, and agent context for coder mode

For OpenAI-compatible providers, telemetry token usage comes from the API's own
reported `usage` fields. It is not estimated locally for the session file.

Tool execution is tracked separately from LLM usage:

- `llm_turn`, `stage_llm`, and `task_llm_turn` carry provider usage
- `tool_exec` carries tool arguments, results, duration, and outcome classification

Recoverable tool misses, such as a large-file `read_file` call that needs a line
range, are marked as `recoverable_error` instead of looking like terminal workflow
failures.

## `apex stats`

Inspect telemetry from the CLI:

```bash
apex stats
apex stats -by-model
apex stats -by-session
apex stats -trace 20
apex stats -session <id>
```

These commands roll up only real LLM events; standalone tool-execution events are
not counted as model token usage.

## CLI reference

```text
apex [flags] [prompt]      run interactively, one-shot, or from a pipe
apex stats [flags]         show telemetry and usage rollups
apex sessions [flags]      list recent sessions
apex mcp [flags]           manage MCP servers
```

## Troubleshooting notes

- If the header `tok` value looks high, remember it is the net session token total,
  not the latest single-call token count.
- If coder mode behaves oddly, inspect both the session `telemetry.json` and the
  matching workflow JSON under `workflows/`.
- If a request exceeds budget, see [CONFIGURATION.md](./CONFIGURATION.md) for the
  current default-vs-explicit budgeting behavior.

## Related docs

- [Configuration](./CONFIGURATION.md)
- [Architecture](./ARCHITECTURE.md)
- [Extensibility](./EXTENSIBILITY.md)
