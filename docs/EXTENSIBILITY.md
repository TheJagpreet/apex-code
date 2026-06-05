# Extensibility

`apex-code` now supports three extension paths:

1. `apex.toml` for project and user configuration
2. MCP servers for external tool/resource catalogs
3. Native Go plugins that register custom tools

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
telemetry, workflows, and indexes under `.apex/`. `state_path` remains
available as a compatibility alias for older configs.

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
