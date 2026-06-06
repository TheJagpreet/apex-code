# User Guide

This guide covers the day-to-day user-facing surface of `apex-code`: the TUI,
chat mode, coder mode, commands, and tools.

## Interactive TUI

The TUI is a full Bubble Tea workspace rather than a scrolling log:

- Branded landing banner that collapses into a compact header once a session is active
- Compact top bar with session id, mode, companion, token count, and cwd
- Height-bounded scrollable transcript with mouse wheel, `PgUp`/`PgDn`, `Home`/`End`,
  and arrow-key navigation when the composer is empty
- Markdown rendering for assistant replies
- Auxiliary panes for tools, diffs, context, stats, help, and the coder plan
- Multiline composer with `Shift+Enter`

The live `tok` value in the header is the net session token total. It updates
continuously from shared session telemetry while chat mode or coder mode is making
LLM calls, rather than waiting for the next transcript event.

## Chat mode and coder mode

`apex-code` has two interaction modes:

- `chat`: the normal conversational tool-using loop
- `coder`: a workflow-backed mode for larger tasks

Switch directly with:

- `/chat`
- `/coder`

## Coder mode

Coder mode adds a workflow-oriented execution path for longer jobs:

1. The planner emits a JSON workflow with phases and tasks.
2. You review the plan in the TUI.
3. You can revise it with `/replan <feedback>`.
4. You approve it with `/approve`, which starts execution.
5. Tasks run through specialized agents such as `architecture`, `solutioner`,
   `tester`, and `reviewer`.

Workflow JSON files are stored under the active session folder in `workflows/`.

## Slash commands

| Command | What it does |
|---|---|
| `/help` | Show the command reference |
| `/explain` `/review` `/fix` `/test` | Insert a prompt starter |
| `/model [name]` | Show or switch the active model |
| `/chat` · `/coder` | Switch directly between chat mode and coder mode |
| `/plan` | Print the current coder workflow plan into chat |
| `/approve` | Approve the current coder-mode plan and start execution |
| `/replan <feedback>` | Ask the planner to revise the current plan |
| `/runplan` | Continue executing the current approved workflow |
| `/resume [id]` · `/sessions` · `/new` | Session management |
| `/pane [name]` | Switch the auxiliary pane |
| `/pin` · `/unpin` | Pin or unpin files into the visible working set |
| `/stats` | Focus the stats pane |
| `/prompts` | List prompt starters |
| `/companion` | Switch the footer companion |
| `/theme [name]` | Cycle or set the color theme |
| `/verbose` | Toggle expanded technical detail |
| `/clear` · `/quit` | Clear the transcript or exit |

## File references

Type `@` in the composer for fuzzy file completion from a gitignore-aware index
of the project. Referenced files are passed to the agent as exact relative paths.

## Tools

Built-in tools currently include:

- `read_file`
- `write_file`
- `edit`
- `list_dir`
- `glob`
- `grep`
- `run`
- `fetch_web`
- `fetch_raw`
- `fetch_json`
- `clone_repo`

Tool activity appears in the tools pane, and edits surface in the diffs pane before
they land.

## Sessions in the TUI

Useful session flows:

- `/new` starts a clean session
- `/sessions` lists recent sessions
- `/resume` resumes a previous session

Session storage and telemetry are covered in [OPERATIONS.md](./OPERATIONS.md).

Outside the TUI, `apex stats` and `apex --stats` read the current project's
`.apex/` directory by default, generate a browser dashboard at
`.apex/stats/index.html`, and open it automatically. Use `-data-dir` if you
want to inspect a different artifact root.

## Related docs

- [Configuration](./CONFIGURATION.md)
- [Operations](./OPERATIONS.md)
- [Architecture](./ARCHITECTURE.md)
