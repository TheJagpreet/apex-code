# apex-code Architecture

This document describes how `apex-code` is structured today: the runtime layers,
how a request flows through the system, where token efficiency is enforced, and
which subsystems are responsible for editing, retrieval, persistence, and UI.

## Design goals

`apex-code` is built around a few constraints:

- It should work well with small, local models, especially through Ollama.
- It should spend as few tokens as possible on deterministic work.
- It should keep the prompt window curated instead of replaying the entire chat.
- It should remain portable as a single Go binary with local state in SQLite.

Those goals shape nearly every package in the repo.

## System overview

At a high level, the application is layered like this:

```text
CLI / TUI
  -> dependency wiring
  -> agent loop
  -> context manager + prompt assembler
  -> provider layer + tool dispatcher
  -> retrieval / diff / persistence subsystems
  -> SQLite-backed session, cache, and telemetry state
```

The main entrypoint lives in `cmd/apex`, while most of the implementation sits
under `internal/`.

## Top-level package map

### `cmd/apex`

The binary entrypoint. It hands off immediately to the CLI package.

### `internal/cli`

Responsible for:

- parsing flags and choosing interactive, one-shot, or pipe mode
- resolving config and feature flags
- constructing providers, tool registries, sessions, telemetry, and skills
- adapting the dependency graph to the TUI agent interface

This package is the composition root for the app.

### `internal/tui`

Implements the Bubble Tea terminal workspace:

- transcript rendering
- slash commands and `@file` completion
- tool, diff, context, stats, and help panes
- streaming assistant output effects
- footer companion and themes
- transcript message selection and clipboard copy support

The TUI is a view/controller layer. It does not own agent logic directly.

### `internal/agent`

Contains the orchestration loop:

- request assembly
- provider invocation
- tool-call execution
- observation of tool results
- termination rules
- budget accounting and prompt preparation

This is the execution engine that turns a user request into one or more model
turns plus real tool actions.

### `internal/provider`

Defines the provider abstraction and concrete model backends:

- `ollama`
- `openai`
- `anthropic`
- `fake`
- `sse`

All providers normalize completions, streaming events, tool calls, token usage,
and stop reasons into shared domain types so the agent loop can stay provider
agnostic.

### `internal/domain`

Holds core shared types such as messages, tool calls/results, requests, and
responses. This package provides the contract between the agent loop, providers,
and tools.

### `internal/contextmgr`

Implements prompt compaction and curated context assembly. This is one of the
most important token-efficiency layers in the repository.

Its responsibilities include:

- tracking a working set derived from messages and tool activity
- rendering a bounded prompt from named token pools
- compacting oversized prompts
- summarizing or eliding stale context
- enforcing output headroom

### `internal/promptasm`

Builds the actual provider request from the current system messages, history,
latest user turn, fresh tool results, and tool schemas.

This package focuses on deterministic prompt layout and stable ordering so the
prefix remains as cache-friendly as possible.

### `internal/tools`

Contains the built-in tool system:

- registry and dispatcher
- lazy tool/schema loading
- tool routing
- safety/result gating
- concrete tools such as `read_file`, `write_file`, `edit`, `grep`, `glob`,
  `list_dir`, `run`, `fetch`, and MCP wrappers

The tools package is designed to keep raw tool output small, structured, and
useful before it ever re-enters model context.

### `internal/diffengine`

Handles targeted edits safely:

- anchor-based hunk application
- fuzzy matching fallback
- rollback on failure
- verification hooks
- filtered failure reporting

It allows the model to propose small edits rather than rewriting whole files.

### `internal/repoindex`

Provides repository indexing and retrieval support:

- filesystem walking
- symbol extraction
- outline-first retrieval
- incremental indexing
- SQLite-backed storage for search and map generation

This subsystem helps the agent orient within a repo without stuffing large files
into context by default.

### `internal/session`

Persists curated sessions and their working-set snapshots. Resume is based on
reconstructing compact state, not replaying the full raw transcript.

### `internal/telemetry`

Stores token, cache, and latency metrics in SQLite and powers:

- `apex stats`
- per-session rollups
- per-model rollups
- recent trace views
- token-savings accounting

### `internal/skills`

Discovers and activates skill bundles that can advertise additional prompt
fragments or tool sets without eagerly inflating the prompt.

### `internal/mcp`

Implements the Model Context Protocol client integration used to expose external
tool servers through the same tool abstraction as native tools.

### `internal/tokenizer`

Provides token counting implementations and heuristics so budget checks can be
performed before provider calls are made.

## Runtime flow

### 1. Startup and mode selection

`internal/cli` determines whether the process should run as:

- interactive TUI
- one-shot prompt execution
- piped stdin execution
- stats / sessions / MCP utility subcommands

It then builds the dependency graph: provider, tool registry, dispatcher,
context manager, session store, telemetry store, skill loader, and lazy tool
router.

### 2. Conversation seeding

Before a conversation runs, the CLI/deps layer creates:

- the base system instruction
- optional lazy tool catalogue text
- optional session state restored from persistence

Interactive mode keeps these messages alive across turns via `tuiAgent`.

### 3. Prompt preparation

For each iteration, the agent loop:

- asks the tool provider for the currently advertised tool schemas
- assembles a structured provider request via `promptasm`
- counts/measures token usage
- checks the result against configured budget pools

If the request exceeds the prompt budget, the context manager compacts it before
the provider is called.

### 4. Model execution

The selected provider streams back events:

- assistant text deltas
- tool calls
- usage updates
- completion/stop signals

The loop collects these into a normalized response.

### 5. Tool execution

If tool calls are present, the dispatcher resolves each tool by name and invokes
it through the native tool interface.

Each tool returns a compact `Result` that is already filtered through the shared
gate. That means long outputs are trimmed, summarized, or tailed before they are
placed back into the conversation.

### 6. Observation and continuation

Tool results are appended as a tool-role message and the loop repeats. If the
assistant returns no further tool calls, the final answer is emitted to either
the TUI or stdout.

## Token-efficiency architecture

Token efficiency is not isolated to one package. It is enforced across several
layers:

### Budgeted prompt pools

The agent loop and context manager divide the prompt into named pools such as:

- system
- tools
- history
- retrieved context
- working files
- output headroom

This prevents any single class of context from consuming the entire window.

### Curated working set

The context manager treats context as a reconstructed view, not a raw append-only
log. Older or less useful information can be digested, summarized, or evicted.

### Tool output gating

Native tool output is never blindly dumped back into context. The tools layer
caps output size and returns compact summaries.

### Outline-first retrieval

The repo indexer prefers signatures and outlines to full file bodies, which is
especially important for large repositories and small-context models.

### Lazy tool and skill loading

When enabled, only compact tool descriptors are initially advertised. Full tool
schemas are injected only when the conversation actually needs them.

### Stable prompt assembly

`promptasm` orders sections from stable to volatile so providers that support
caching, or local runtimes benefiting from warm prefixes, can reuse more work.

## Editing architecture

Code edits pass through two layers:

### Tool layer

The `edit` and `write_file` tools validate paths, normalize arguments, and
return compact results. They also surface hash mismatches with current hash
details so the model can recover instead of failing blindly.

### Diff engine

The diff engine applies anchored hunks, supports fuzzy fallback, and can run
verification commands after edits. If verification fails, it rolls changes back
and returns a filtered failure summary instead of flooding the model with raw
output.

This keeps editing safe, surgical, and relatively cheap in prompt terms.

## Retrieval and repository awareness

The repo indexer exists to answer questions like:

- which files matter for this feature?
- what symbols exist here?
- what does the high-level repo map look like?

Its output feeds the `retrieved-context` pool rather than bypassing the prompt
budget. That is a key architectural choice: retrieval is useful, but never
allowed to become unbounded context.

## Persistence model

`apex-code` uses SQLite for local state so the binary stays self-contained.

State includes:

- session records and turn summaries
- working-set snapshots for resume
- telemetry and token metrics
- index and cache data

The system is intentionally local-first. It does not depend on a remote service
for normal operation.

## UI architecture

The TUI and one-shot flows share the same underlying agent runtime.

The TUI adds:

- transcript rendering and navigation
- streaming visuals
- local interaction commands
- inspection panes for tools, diffs, context, and stats
- model/session affordances

This keeps the product surface richer without duplicating core execution logic.

## Extensibility

The architecture has multiple extension points:

- new providers via the provider interface
- new native Go tools via the tool interface/registry
- MCP tools via the MCP wrapper layer
- skills via lazy-discovered bundles

The shared registry, dispatcher, and gating model allow extensions to fit into
the same safety and token-budget rules as built-in tools.

## Testing strategy

The repository emphasizes package-level and integration-style tests:

- provider conformance tests
- agent loop tests
- context manager tests
- tool behavior and output-capping tests
- diff engine tests
- TUI rendering and keyboard-flow tests
- telemetry/session persistence tests

The `fake` provider is particularly important because it allows deterministic
testing of agent behavior without depending on live model backends.

## Current tradeoffs and future work

Some architecture decisions remain intentionally pragmatic:

- lexical/local retrieval is favored over mandatory embeddings
- small-model friendliness is prioritized over maximal agent complexity
- the TUI already exposes diffs and runtime detail, but richer approval UX can
  still be expanded

The existing structure leaves room for more providers, richer skills, stricter
approval flows, and deeper retrieval strategies without changing the overall
shape of the system.
