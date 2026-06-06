# Configuration

`apex-code` reads configuration from flags, environment, `.env`, and `apex.toml`.

## Resolution order

Configuration is resolved in this order:

1. CLI flags
2. Process environment variables
3. Project-local `.env`
4. Project `apex.toml`
5. User config `apex.toml`
6. Built-in defaults

Existing shell environment variables still override values loaded from `.env`.

## Provider selection

Provider selection is automatic:

- If `APEX_PROVIDER=openai` is set, apex uses the OpenAI-compatible provider
- If `OPENAI_API_KEY` is present and no provider is forced, apex defaults to OpenAI
- Otherwise apex defaults to Ollama

Common OpenAI setup:

```dotenv
APEX_PROVIDER=openai
OPENAI_API_KEY=sk-...
APEX_MODEL=gpt-4o-mini
```

Common Ollama setup:

```dotenv
APEX_PROVIDER=ollama
APEX_MODEL=gemma4:e2b
APEX_BASE_URL=http://localhost:11434
```

## Flags and environment variables

| Flag | Env | Default | Description |
|---|---|---|---|
| `-provider` | `APEX_PROVIDER` | `ollama` | Provider backend (`ollama` or `openai`) |
| `-model` | `APEX_MODEL` | provider-specific | Model name |
| `-base-url` | `APEX_BASE_URL` | provider-specific | Provider base URL |
| `-max-iterations` | `APEX_MAX_ITERATIONS` | `50` | Max agent loop turns |
| `-lazy-tools` | `APEX_LAZY_TOOLS` | `false` | Advertise tool names and load schemas on demand |
| `-skills` | `APEX_SKILLS_DIR` | `./skills` | Skill bundles directory |
| `-data-dir` | `APEX_DATA_DIR` | `./.apex` | Base directory for sessions, workflows, telemetry, and indexes |
| `-resume` | `APEX_RESUME` | — | Resume a prior session |
| `-verbose` | — | `false` | Expanded technical detail in the TUI |
| `-tui` / `-one-shot` | — | auto | Force interactive or non-interactive mode |
| — | `OPENAI_API_KEY` / `APEX_API_KEY` | — | API key for the OpenAI provider |

See [`apex.toml.example`](./apex.toml.example) for a sample config file.

## Budgeting

Budgeting has two modes.

### Default mode

If no `APEX_BUDGET_*` variables are set:

- apex uses most of the provider context window
- apex keeps only a modest output reserve
- no strict pool split is enforced

This is the recommended default for most users.

### Explicit mode

If one or more `APEX_BUDGET_*` values are set, apex switches to strict pool-based
budgeting across:

- system
- tools
- history
- retrieved context
- working files
- output headroom

Budget variables:

`APEX_BUDGET_SYSTEM`  
`APEX_BUDGET_TOOLS`  
`APEX_BUDGET_HISTORY`  
`APEX_BUDGET_RETRIEVED`  
`APEX_BUDGET_WORKING_FILES`  
`APEX_BUDGET_OUTPUT_HEADROOM`

Example:

```dotenv
APEX_BUDGET_SYSTEM=0.10
APEX_BUDGET_TOOLS=0.10
APEX_BUDGET_HISTORY=0.40
APEX_BUDGET_RETRIEVED=0.15
APEX_BUDGET_WORKING_FILES=0.15
APEX_BUDGET_OUTPUT_HEADROOM=0.10
```

## On-disk layout

By default apex stores state under `.apex/`:

```text
.apex/
  sessions/
    <session-id>/
      session.json
      telemetry.json
      workflows/
        <timestamp>-<session-id>-<workflow-id>.json
```

`apex sessions`, `apex stats`, resume loading, and coder workflow lookup all read
from this `sessions/` tree directly.

If you do not pass `-data-dir`, apex uses `.apex/` in the current working
directory. To use a different artifact root, pass `-data-dir` or set
`APEX_DATA_DIR`.

This also controls where the browser stats dashboard is written:

```text
.apex/
  stats/
    index.html
```

## Related docs

- [User guide](./USER_GUIDE.md)
- [Operations](./OPERATIONS.md)
- [Architecture](./ARCHITECTURE.md)
