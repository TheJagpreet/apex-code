# apex-code Architecture

This document describes how `apex-code` is structured today: how configuration
is resolved, how prompts move through the runtime, how tool use and coder mode
work, and where token budgeting and compaction are enforced.

## Design principles

`apex-code` is built around a few practical constraints:

- It should run as a single local Go binary.
- It should work with multiple providers through one shared adapter interface.
- It should spend tokens on model reasoning, not deterministic plumbing.
- It should treat context as curated state, not an append-only transcript.
- It should keep real workspace actions inspectable through tools, diffs, and telemetry.

## Runtime overview

At a high level, the runtime looks like this:

```text
CLI / TUI
  -> config resolution + provider selection
  -> dependency wiring
  -> agent loop
  -> prompt assembly + budgeting
  -> provider stream
  -> tool execution
  -> context compaction / persistence / telemetry
```

Coder mode adds one more layer on top:

```text
user prompt
  -> orchestrator enrichment
  -> planner JSON workflow
  -> user review / approve
  -> task execution via role-specific agent runs
  -> workflow persistence + TUI plan pane
```

## Configuration flow

Configuration is resolved in this order:

1. CLI flags
2. Process environment variables
3. Project-local `.env`
4. Project `apex.toml`
5. User config `apex.toml`
6. Built-in defaults

Important current behavior:

- Provider selection defaults to `openai` if an API key is present and no provider
  is forced.
- Otherwise it defaults to `ollama`.
- Budgeting also has two modes:
  - no explicit `APEX_BUDGET_*` values: use most of the model window automatically
  - explicit `APEX_BUDGET_*` values: enforce pool-based budgeting

The config logic lives primarily in:

- `internal/config`
- `internal/cli`

## Package map

### `cmd/apex`

Binary entrypoint. It immediately hands off to `internal/cli`.

### `internal/cli`

The composition root for the application.

Responsibilities:

- parse flags
- resolve config
- choose one-shot / pipe / TUI mode
- build the provider, tool registry, session store, telemetry store, workflow store,
  and context manager
- adapt the runtime into the TUI-facing agent interface

### `internal/provider`

Defines the provider abstraction used everywhere else.

Current adapters:

- `ollama`
- `openai`
- `anthropic`
- `fake`

Every provider maps vendor-specific streaming responses into the shared event model:

- text deltas
- tool calls
- usage updates
- stop reasons

This is the main reason chat mode and coder mode are supposed to behave consistently:
they share the same provider interface and agent loop.

### `internal/domain`

Shared contracts used across the app:

- messages
- tool calls
- tool results
- requests
- responses
- usage
- coder workflow structures

### `internal/agent`

The core execution loop.

Responsibilities:

- assemble each request
- enforce max iterations
- stream model output
- dispatch tool calls
- append tool results back into the conversation
- terminate with final answer / max iterations / error / user cancel
- enforce prompt budgeting before each provider call

The agent loop is used both by normal chat and by coder-mode task execution.

### `internal/contextmgr`

Curates the model window from messages instead of replaying everything forever.

Responsibilities:

- convert messages into a working set
- measure prompt size
- elide duplicates
- digest stale tool results
- summarize older history
- evict low-priority context until the prompt fits
- return a compacted message list back to the agent loop

This is the implementation behind the compactor hook used by the agent.

### `internal/promptasm`

Deterministically assembles provider requests from:

- system messages
- tool descriptors / schemas
- older history
- latest user message
- fresh tool messages

It keeps stable prompt sections ahead of volatile ones so the structure is predictable.

### `internal/tools`

Owns the built-in tool system:

- registry
- dispatcher
- lazy tool activation
- MCP wrapping
- output/result shaping

Built-in tools currently include file reads/writes/edits, search, shell execution,
directory listing, globbing, and URL fetch.

### `internal/codermode`

Implements workflow-backed coding mode.

Responsibilities:

- create workflow JSON from a user prompt
- run orchestrator enrichment
- run planner JSON generation
- support replan / approve / execute
- execute tasks through role-specific prompts
- persist workflow state and run history

Coder mode does not bypass the normal runtime. It uses the same provider + tool
infrastructure, but wraps it in a higher-level workflow state machine.

### `internal/tui`

Implements the Bubble Tea interface:

- transcript rendering
- streaming weave effect
- panes for tools, diffs, stats, context, help, and plan
- slash commands
- `@file` completion
- themes and footer companion
- session and model controls

### `internal/session`

Persists session records and compact working-set snapshots so resume restores a
curated state rather than replaying every raw turn.

### `internal/telemetry`

Stores append-only, file-backed session telemetry and powers `apex stats`.
Each session event can carry the exact provider-bound `input_messages`, the raw
assistant `output_message`, structured tool-call details including arguments,
tool-result payloads, and provider-reported token usage.

### `internal/repoindex`

Supports repository walking and indexing for file-aware interaction and completion.

### `internal/mcp`

Wraps Model Context Protocol servers so external tools can join the same tool path
as native tools.

### `internal/skills`

Discovers and activates skill bundles that can add context or tool capability without
eagerly inflating the prompt.

## Request flow

### 1. Startup

The CLI resolves config, loads `.env` if present, selects the provider, and builds
the dependency graph.

### 2. Conversation seeding

The runtime seeds:

- base system instructions
- lazy tool catalogue if enabled
- resumed session state if requested

### 3. Request assembly

Before each provider call, the agent loop:

- optionally refreshes lazy tool schemas
- assembles messages through `promptasm`
- measures prompt size
- builds a budget from provider caps + runtime options

### 4. Budget check

If the prompt is too large:

- the compactor runs
- the context manager tries to shrink the prompt
- the agent retries assembly

If repeated compaction still cannot fit the prompt, the run stops with budget exhaustion.

### 5. Provider call

The provider streams back:

- text
- tool calls
- usage
- done events

### 6. Tool execution

If tool calls are emitted:

- the dispatcher resolves each tool
- tool results are compacted through the tool result format/gate
- results are appended as `tool` messages
- the loop continues

### 7. Final answer or workflow step completion

If no more tool calls are emitted:

- normal chat returns a final response
- coder mode records the task result into workflow history

## Budgeting model

Budgeting now has two distinct modes.

### Default mode: near-full-window

If no `APEX_BUDGET_*` values are explicitly configured:

- apex uses almost the entire provider context window
- it reserves only a modest output headroom
- it does not enforce strict pool splits

This is the current default because it behaves better for larger repo-analysis tasks.

### Explicit mode: pool-based budgeting

If one or more `APEX_BUDGET_*` values are configured:

- apex divides the context window into named pools:
  - system
  - tools
  - history
  - retrieved context
  - working files
  - output headroom
- each pool gets a fraction of the total window
- prompt assembly is checked against both total prompt limit and pool limits

This mode is better when you want predictable shaping across turns.

## What the compactor is

The compactor is the shrinking step used when a request is too large.

In practice, it is the `contextmgr.Compactor`, which:

- rebuilds a working set from the current messages
- rewrites stale or redundant items
- summarizes older history when useful
- evicts lower-priority content
- hands a smaller message list back to the agent loop

The agent loop gives it up to a few attempts before failing the run.

## Tool-call compatibility rules

For OpenAI-compatible providers, tool-call history has to remain structurally valid:

- assistant message with `tool_calls`
- followed by matching `tool` message(s) with the same `tool_call_id`

This matters especially for DeepSeek and other strict OpenAI-compatible backends.
The current adapter sanitizes malformed historical tool-call blocks before sending
them so compacted history does not poison later requests.

## Coder mode execution model

Coder mode introduces a persisted workflow layer.

Sequence:

1. User enters a long-running coding request
2. Orchestrator enriches the request
3. Planner emits strict JSON tasks/phases
4. Workflow is written to `.apex/sessions/<session-id>/workflows/`
5. User reviews in the TUI plan pane
6. `/approve` starts task execution
7. Each task is executed through the shared agent loop with a role-specific prompt
8. Results are appended to workflow history and shown in the plan pane

So coder mode is not a second independent backend. It is a workflow layer above
the same provider/tool/agent infrastructure used by chat mode.

## Persistence model

Local state is intentionally simple and local-first.

Persistent data includes:

- session records in `session.json`
- curated working-set snapshots in the same session artifact
- workflow JSON files under each session's `workflows/` directory
- append-only `telemetry.json` files with timestamped LLM and tool events

This keeps the application portable and inspectable.

## Telemetry model

Telemetry records:

- prompt / completion / total tokens
- cache creation / cache read tokens when available
- per-turn latency
- exact request messages sent to the provider
- exact assistant output returned by the provider
- tool call details including ids, names, and raw arguments
- tool result payloads
- model rollups
- session rollups
- savings attributed to lazy tools or compaction
- workflow/task/agent context for coder-mode turns

For the OpenAI-compatible adapter, token usage is taken from the provider's
reported `usage` fields on the completion response stream, not from local token
estimation.
- tool-call names and tool-result counts

The TUI status bar token counter is a session-level running total rather than a
single-request snapshot.

## Current practical tradeoffs

- Provider caps for some OpenAI-compatible backends are still conservative defaults.
- Context compaction is intentionally deterministic, not model-driven by default.
- Tool-heavy repo-analysis tasks can still hit max iterations if the model keeps
  exploring instead of converging.
- The system prioritizes inspectability and deterministic tool usage over maximal autonomy.

## Related docs

- [`README.md`](../README.md)
- [`docs/EXTENSIBILITY.md`](./EXTENSIBILITY.md)
- [`docs/PATCH_FORMAT.md`](./PATCH_FORMAT.md)
