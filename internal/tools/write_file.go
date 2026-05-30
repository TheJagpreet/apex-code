package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type WriteFileTool struct {
	gate Gate
}

type WriteFileArgs struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

func NewWriteFileTool(gate Gate) Tool {
	return &WriteFileTool{gate: gate}
}

func (t *WriteFileTool) Name() string { return "write_file" }

func (t *WriteFileTool) Description() string {
	return "Create a file or overwrite only when the expected file hash matches."
}

func (t *WriteFileTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":            map[string]any{"type": "string"},
			"content":         map[string]any{"type": "string"},
			"expected_sha256": map[string]any{"type": "string"},
		},
		"required": []string{"path", "content"},
	})
}

func (t *WriteFileTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 200
}

func (t *WriteFileTool) Invoke(_ context.Context, raw json.RawMessage) (Result, error) {
	var args WriteFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	args.Path = stripPathSigil(args.Path)
	path, err := resolvePath(args.Path)
	if err != nil {
		return Result{}, err
	}

	if args.ExpectedSHA256 != "" {
		info, err := os.Stat(path)
		if err != nil {
			res, mismatchErr := renderHashMismatch(path, args.ExpectedSHA256)
			res.ToolName = t.Name()
			return t.gate.Apply(res), mismatchErr
		}
		if info.IsDir() {
			return Result{}, fmt.Errorf("path is a directory: %s", args.Path)
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return Result{}, err
		}
		if sum != args.ExpectedSHA256 {
			res, mismatchErr := renderHashMismatch(path, args.ExpectedSHA256)
			res.ToolName = t.Name()
			return t.gate.Apply(res), mismatchErr
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return Result{}, err
	}

	summary := fmt.Sprintf("wrote %s", args.Path)
	if args.ExpectedSHA256 == "" {
		summary = fmt.Sprintf("wrote %s without expected_sha256", args.Path)
	}
	return t.gate.Apply(Result{
		ToolName: t.Name(),
		Payload:  fmt.Sprintf("wrote %s\nbytes: %d", filepath.ToSlash(path), len(args.Content)),
		Summary:  summary,
	}), nil
}
