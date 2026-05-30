package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apex-code/apex/internal/diffengine"
)

type EditTool struct {
	gate Gate
}

type EditArgs struct {
	Path           string                 `json:"path,omitempty"`
	Old            string                 `json:"old,omitempty"`
	New            string                 `json:"new,omitempty"`
	ExpectedSHA256 string                 `json:"expected_sha256,omitempty"`
	Files          []diffengine.FilePatch `json:"files,omitempty"`
	VerifyCommands []string               `json:"verify_commands,omitempty"`
	Format         bool                   `json:"format,omitempty"`
}

func NewEditTool(gate Gate) Tool {
	return &EditTool{gate: gate}
}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Description() string {
	return "Apply compact anchor patches atomically, with rollback and compact verification failures."
}

func (t *EditTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":            map[string]any{"type": "string"},
			"old":             map[string]any{"type": "string"},
			"new":             map[string]any{"type": "string"},
			"expected_sha256": map[string]any{"type": "string"},
			"format":          map[string]any{"type": "boolean"},
			"verify_commands": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"files": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":            map[string]any{"type": "string"},
						"expected_sha256": map[string]any{"type": "string"},
						"hunks": map[string]any{"type": "array", "items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old":           map[string]any{"type": "string"},
								"new":           map[string]any{"type": "string"},
								"anchor_before": map[string]any{"type": "string"},
								"anchor_after":  map[string]any{"type": "string"},
							},
							"required": []string{"old", "new"},
						}},
					},
					"required": []string{"path", "hunks"},
				},
			},
		},
	})
}

func (t *EditTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 220
}

func (t *EditTool) Invoke(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args EditArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	args.Path = stripPathSigil(args.Path)
	for i := range args.Files {
		args.Files[i].Path = stripPathSigil(args.Files[i].Path)
	}
	patch := diffengine.Patch{
		Files:          args.Files,
		VerifyCommands: args.VerifyCommands,
		Format:         args.Format,
	}
	if len(patch.Files) == 0 {
		if args.Path == "" || args.Old == "" {
			return Result{}, fmt.Errorf("edit requires files[] or path/old/new")
		}
		patch.Files = []diffengine.FilePatch{{
			Path:           args.Path,
			ExpectedSHA256: args.ExpectedSHA256,
			Hunks:          []diffengine.Hunk{{Old: args.Old, New: args.New}},
		}}
	}
	for _, file := range patch.Files {
		if file.ExpectedSHA256 == "" {
			continue
		}
		path, err := resolvePath(file.Path)
		if err != nil {
			return Result{}, err
		}
		current, err := fileSHA256(path)
		if err == nil && current == file.ExpectedSHA256 {
			continue
		}
		res, mismatchErr := renderHashMismatch(path, file.ExpectedSHA256)
		res.ToolName = t.Name()
		return t.gate.Apply(res), mismatchErr
	}
	engine := diffengine.New(diffengine.Options{Root: ".", Format: args.Format})
	applied, err := engine.Apply(ctx, patch)
	payload := renderEditResult(applied, err)
	res := t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   payload,
		Summary:   fmt.Sprintf("edit files=%d hunks=%d rollback=%t", len(applied.AppliedFiles), applied.HunksApplied, applied.RolledBack),
		Truncated: false,
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

func renderEditResult(result diffengine.Result, err error) string {
	lines := []string{
		"edited",
		fmt.Sprintf("applied_files: %d", len(result.AppliedFiles)),
		fmt.Sprintf("hunks_applied: %d", result.HunksApplied),
		fmt.Sprintf("rolled_back: %t", result.RolledBack),
	}
	for _, path := range result.AppliedFiles {
		lines = append(lines, "file: "+path)
	}
	if result.Verification.Command != "" {
		lines = append(lines, "verify_command: "+result.Verification.Command)
		lines = append(lines, fmt.Sprintf("verify_exit_code: %d", result.Verification.ExitCode))
	}
	if err != nil {
		lines = append(lines, "error: "+err.Error())
	}
	if result.Verification.Output != "" {
		lines = append(lines, "failure_output:")
		lines = append(lines, result.Verification.Output)
	}
	return joinLines(lines)
}
