package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

type GlobTool struct {
	gate Gate
}

type GlobArgs struct {
	Pattern string `json:"pattern"`
	Cap     int    `json:"cap,omitempty"`
}

func NewGlobTool(gate Gate) Tool {
	return &GlobTool{gate: gate}
}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find local file paths by glob pattern. Use this for discovery when you know the filename shape."
}

func (t *GlobTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
			"cap":     map[string]any{"type": "integer", "minimum": 1},
		},
		"required": []string{"pattern"},
	})
}

func (t *GlobTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 120
}

func (t *GlobTool) Invoke(_ context.Context, raw json.RawMessage) (Result, error) {
	var args GlobArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	args.Pattern = stripPathSigil(args.Pattern)
	if strings.TrimSpace(args.Pattern) == "" {
		return Result{}, fmt.Errorf("pattern must not be empty")
	}

	capItems := args.Cap
	if capItems <= 0 {
		capItems = defaultGlobCap
	}

	matches := make([]string, 0)
	if strings.Contains(args.Pattern, "**") {
		root, pattern := splitRecursivePattern(args.Pattern)
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if _, collapsed := collapsedDirNames[d.Name()]; collapsed && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			ok, _ := filepath.Match(pattern, filepath.ToSlash(rel))
			if ok {
				matches = append(matches, filepath.ToSlash(path))
			}
			return nil
		})
	} else {
		found, err := filepath.Glob(args.Pattern)
		if err != nil {
			return Result{}, err
		}
		for _, match := range found {
			matches = append(matches, filepath.ToSlash(match))
		}
	}

	sort.Strings(matches)
	truncated := false
	if len(matches) > capItems {
		remaining := len(matches) - capItems
		matches = append(matches[:capItems], fmt.Sprintf("+%d more matches", remaining))
		truncated = true
	}

	return t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   joinLines(matches),
		Summary:   fmt.Sprintf("glob %s", args.Pattern),
		Truncated: truncated,
	}), nil
}

func splitRecursivePattern(pattern string) (string, string) {
	pattern = filepath.ToSlash(pattern)
	idx := strings.Index(pattern, "**")
	if idx == -1 {
		return ".", pattern
	}
	root := strings.TrimSuffix(pattern[:idx], "/")
	if root == "" {
		root = "."
	}
	rest := strings.TrimPrefix(pattern[idx+2:], "/")
	if rest == "" {
		rest = "*"
	}
	return filepath.FromSlash(root), rest
}
