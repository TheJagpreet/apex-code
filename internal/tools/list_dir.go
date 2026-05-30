package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type ListDirTool struct {
	gate Gate
}

type ListDirArgs struct {
	Path string `json:"path"`
	Cap  int    `json:"cap,omitempty"`
}

func NewListDirTool(gate Gate) Tool {
	return &ListDirTool{gate: gate}
}

func (t *ListDirTool) Name() string { return "list_dir" }

func (t *ListDirTool) Description() string {
	return "List a directory with heavy folders collapsed and entry count capped."
}

func (t *ListDirTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"cap":  map[string]any{"type": "integer", "minimum": 1},
		},
		"required": []string{"path"},
	})
}

func (t *ListDirTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 120
}

func (t *ListDirTool) Invoke(_ context.Context, raw json.RawMessage) (Result, error) {
	var args ListDirArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	path, err := resolvePath(args.Path)
	if err != nil {
		return Result{}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Result{}, err
	}

	capEntries := args.Cap
	if capEntries <= 0 {
		capEntries = defaultListCap
	}

	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if _, collapsed := collapsedDirNames[name]; collapsed {
			items = append(items, name+"/ (collapsed)")
			continue
		}
		if entry.IsDir() {
			items = append(items, name+"/")
		} else {
			items = append(items, name)
		}
	}
	sort.Strings(items)

	truncated := false
	if len(items) > capEntries {
		remaining := len(items) - capEntries
		items = append(items[:capEntries], fmt.Sprintf("+%d more entries", remaining))
		truncated = true
	}

	return t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   filepath.Clean(args.Path) + "\n" + joinLines(items),
		Summary:   fmt.Sprintf("listed %s", args.Path),
		Truncated: truncated,
	}), nil
}
