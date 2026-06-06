package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

type ReadFileTool struct {
	gate Gate
}

type ReadFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

func NewReadFileTool(gate Gate) Tool {
	return &ReadFileTool{gate: gate}
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Description() string {
	return "Read an exact local file body or line range. Use this for precise source inspection."
}

func (t *ReadFileTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":       map[string]any{"type": "string"},
			"start_line": map[string]any{"type": "integer", "minimum": 1},
			"end_line":   map[string]any{"type": "integer", "minimum": 1},
		},
		"required": []string{"path"},
	})
}

func (t *ReadFileTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 300
}

func (t *ReadFileTool) Invoke(_ context.Context, raw json.RawMessage) (Result, error) {
	var args ReadFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	args.Path = stripPathSigil(args.Path)
	path, err := resolvePath(args.Path)
	if err != nil {
		return Result{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("path is a directory: %s", args.Path)
	}
	if info.Size() > defaultReadForceRangeBytes && args.StartLine == 0 && args.EndLine == 0 {
		return Result{}, fmt.Errorf("file too large for full read; provide start_line and end_line")
	}

	lines, err := readLines(path)
	if err != nil {
		return Result{}, err
	}
	sha, err := fileSHA256(path)
	if err != nil {
		return Result{}, err
	}
	start := 1
	end := len(lines)
	if args.StartLine > 0 {
		start = args.StartLine
	}
	if args.EndLine > 0 {
		end = args.EndLine
	}
	if start < 1 || end < start || start > len(lines) {
		return Result{}, fmt.Errorf("invalid line range")
	}
	if end > len(lines) {
		end = len(lines)
	}

	selected := lines[start-1 : end]
	return t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   fmt.Sprintf("path: %s\nsha256: %s\nlines: %d-%d of %d\n%s", args.Path, sha, start, end, len(lines), formatLineRange(selected, start)),
		Summary:   fmt.Sprintf("read %s lines %d-%d sha=%s", args.Path, start, end, sha[:12]),
		Truncated: end-start+1 < len(lines),
		Metadata: map[string]string{
			"path": path,
			"sha":  sha,
		},
	}), nil
}
