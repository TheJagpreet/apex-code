package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apex-code/apex/internal/mcp"
)

type mcpTool struct {
	client *mcp.Client
	tool   mcp.Tool
	gate   Gate
}

func WrapMCPClient(client *mcp.Client, gate Gate) ([]Tool, error) {
	tools, err := client.ListTools(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, mcpTool{client: client, tool: tool, gate: gate})
	}
	return out, nil
}

func (t mcpTool) Name() string { return t.tool.Name }

func (t mcpTool) Description() string {
	return fmt.Sprintf("%s (via MCP %s)", t.tool.Description, t.client.Name())
}

func (t mcpTool) Schema() json.RawMessage {
	if len(t.tool.InputSchema) == 0 {
		return mustJSONSchema(map[string]any{"type": "object"})
	}
	return append(json.RawMessage(nil), t.tool.InputSchema...)
}

func (t mcpTool) Invoke(ctx context.Context, args json.RawMessage) (Result, error) {
	payload, err := t.client.CallTool(ctx, t.tool.Name, args)
	if err != nil {
		return t.gate.Apply(Result{
			ToolName: t.tool.Name,
			Payload:  err.Error(),
			Summary:  "MCP tool call failed",
			IsError:  true,
		}), err
	}
	return t.gate.Apply(Result{
		ToolName: t.tool.Name,
		Payload:  payload,
		Summary:  "MCP tool result",
	}), nil
}

func (t mcpTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 64
}
