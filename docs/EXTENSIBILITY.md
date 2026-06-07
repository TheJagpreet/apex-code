# Extensibility

`apex-code` now supports four extension paths:

1. `apex.toml` for project and user configuration
2. project-local markdown agents and skills under `.apex/`
3. MCP servers for external tool/resource catalogs
4. Native Go plugins that register custom tools

## `apex.toml`

`apex-code` resolves configuration in this order:

1. CLI flags
2. Environment variables
3. Project `.env`
4. Project `apex.toml`
5. User config at `%AppData%/apex-code/apex.toml` on Windows or `~/.config/apex-code/apex.toml` elsewhere
6. Built-in defaults

See [`docs/apex.toml.example`](./apex.toml.example) for a complete sample.

The primary persistence knob is `data_dir`. By default apex stores sessions,
telemetry, workflows, and indexes under `.apex/`.

## Custom agents and skills

Project-local agent and skill bundles live under the repository's `.apex/`
folder:

```text
.apex/
  agents/
    frontend.md
  skills/
    docs.md
```

Both formats use:

1. YAML front matter between leading `---` markers
2. a markdown body below the front matter

### Agent file format

Agent files live under `.apex/agents/*.md`.

Required fields:

- `name`: unique agent name used by `/agent <name>` and `/<name>`
- `description`: one-line summary shown in discovery lists

Optional fields:

- `aliases`: alternate names that can be used as slash commands such as `/ui`
- `skills`: custom skill names that this agent may use on demand

Body:

- Required in practice.
- The markdown body becomes the agent instruction block injected into the runtime.

Full agent example with all fields:

```md
---
name: frontend
description: Focus on UI polish, layout clarity, interaction details, and accessibility
aliases:
  - ui
  - ux
skills:
  - testing
  - docs
---
You are the frontend specialist for this repository.

Priorities:

- Preserve the existing design language when one already exists.
- Improve spacing, typography, responsiveness, and accessibility.
- Prefer the smallest safe UI change that materially improves the result.
```

### Skill file format

Skill files live under `.apex/skills/*.md`.

Required fields:

- `name`: unique skill name used by `#<name>` and for lazy activation
- `description`: one-line summary used in discovery lists and lightweight matching

Optional fields:

- `triggers`: extra keywords or phrases that help the runtime match this skill
- `tools`: tool names to inject when the skill activates

Body:

- Required in practice.
- The markdown body becomes the skill instruction block injected only when the
  skill is activated.

Full skill example with all fields:

```md
---
name: docs
description: Improve README, guides, onboarding text, examples, and developer-facing documentation
triggers:
  - README
  - docs
  - documentation
  - onboarding
tools:
  - read_file
  - edit
  - write_file
  - grep
---
Use this skill when the task is primarily about documentation quality or developer guidance.

Expectations:

- Prefer concise, accurate explanations.
- Keep examples realistic and runnable.
- Update nearby docs when behavior changes.
```

### TUI behavior

Agents:

- `/agents` lists discovered agent files
- `/agent <name>` loads one explicitly
- `/<name>` or `/<alias>` also loads it directly
- `/<name> your prompt here` loads the agent and immediately sends the remaining text as the prompt
- agent-declared `skills:` are not fully loaded just by loading the agent
- instead, apex passes those attached skill descriptions to the active agent first
- the full skill body is only injected later if the skill is explicitly activated or lazily matched while that agent is active

Skills:

- `/skills` lists discovered skill files and shows which are currently loaded
- `#<name>` is the explicit skill syntax in the composer
- `#<name> your prompt here` activates the skill and sends the remaining text as the prompt
- typing `#` in the composer shows dropdown autocomplete, just like `/` and `@`
- lazy auto-activation is limited to the skills attached to the currently active custom agent
- changing mode with `/chat` or `/coder` clears the active custom agent and any loaded custom skills

Skills are also still loaded lazily from metadata matching. Explicit `#skill`
tags force activation even if the prompt text would not otherwise match.

## MCP

Configure one or more MCP servers:

```toml
[[mcp_servers]]
name = "filesystem"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "."]
enabled = true
```

Inspect a configured server:

```powershell
.\bin\apex.exe mcp -server filesystem
```

Wrapped MCP tools are passed through the same output gate as built-in tools, so
large remote responses are still summarized and capped before they re-enter the
agent loop.

## Native Go Plugins

Native plugins implement the `tools.Plugin` interface and register one or more
tools against the shared registry:

```go
type ExamplePlugin struct{}

func (ExamplePlugin) Name() string { return "example" }

func (ExamplePlugin) Register(reg *tools.Registry) error {
	return reg.Register(NewExampleTool())
}
```

Then wire it in during startup:

```go
registry := tools.NewDefaultRegistry()
if err := tools.RegisterPlugins(registry, ExamplePlugin{}); err != nil {
	log.Fatal(err)
}
```

This keeps custom tools on the same execution path as built-ins: they get the
same schema handling, dispatch flow, and output summarization behavior.
