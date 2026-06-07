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
- active custom agent and custom skill file metadata for each chat/tool event
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
apex --stats
apex stats
apex stats -by-model
apex stats -by-session
apex stats -trace 20
apex stats -session <id>
```

Default behavior:

- `apex stats` reads from `.apex/` in the current working directory
- `apex --stats` is a top-level shortcut to open the same dashboard
- use `-data-dir <path>` or `APEX_DATA_DIR` to inspect another artifact root

`apex stats` parses `.apex/sessions/`, writes a dark HTML dashboard to
`.apex/stats/index.html`, and opens it in the default browser.

The dashboard includes:

- overall token, timing, session, workflow, and mode totals
- recent sessions with titles and usage
- model usage
- tool activity and error counts
- coder-agent activity
- recent LLM calls

Only real LLM events count toward model token usage; standalone tool-execution
events are tracked separately in the report.

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
- The header token value is live session telemetry, so it can move before the next
  transcript line appears.
- If coder mode behaves oddly, inspect both the session `telemetry.json` and the
  matching workflow JSON under `workflows/`.
- If a request exceeds budget, see [CONFIGURATION.md](./CONFIGURATION.md) for the
  current default-vs-explicit budgeting behavior.

## Related docs

- [Configuration](./CONFIGURATION.md)
- [Architecture](./ARCHITECTURE.md)
- [Extensibility](./EXTENSIBILITY.md)
