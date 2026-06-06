package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type GrepTool struct {
	gate Gate
}

type GrepArgs struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path,omitempty"`
	ContextLines  int    `json:"context_lines,omitempty"`
	Cap           int    `json:"cap,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
}

func NewGrepTool(gate Gate) Tool {
	return &GrepTool{gate: gate}
}

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Description() string {
	return "Search local files for matching text with compact context. Best for locating symbols, strings, and TODOs."
}

func (t *GrepTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":        map[string]any{"type": "string"},
			"path":           map[string]any{"type": "string"},
			"context_lines":  map[string]any{"type": "integer", "minimum": 0},
			"cap":            map[string]any{"type": "integer", "minimum": 1},
			"case_sensitive": map[string]any{"type": "boolean"},
		},
		"required": []string{"pattern"},
	})
}

func (t *GrepTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 400
}

func (t *GrepTool) Invoke(_ context.Context, raw json.RawMessage) (Result, error) {
	var args GrepArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return Result{}, fmt.Errorf("pattern must not be empty")
	}

	root := "."
	if args.Path != "" {
		var err error
		root, err = resolvePath(args.Path)
		if err != nil {
			return Result{}, err
		}
	}

	contextLines := args.ContextLines
	if contextLines < 0 {
		contextLines = 0
	}
	capMatches := args.Cap
	if capMatches <= 0 {
		capMatches = defaultGrepCap
	}

	pattern := args.Pattern
	if !args.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Result{}, err
	}

	type match struct {
		path    string
		line    int
		score   int
		snippet string
	}
	matches := make([]match, 0)

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

		lines, err := readLines(path)
		if err != nil {
			return nil
		}
		for i, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			start := max(0, i-contextLines)
			end := min(len(lines), i+contextLines+1)
			block := make([]string, 0, end-start+1)
			block = append(block, fmt.Sprintf("%s:%d", filepath.ToSlash(path), i+1))
			for j := start; j < end; j++ {
				block = append(block, fmt.Sprintf("%d: %s", j+1, lines[j]))
			}
			score := 10
			if strings.Contains(filepath.Base(path), re.String()) {
				score += 5
			}
			if strings.Contains(line, args.Pattern) {
				score += 3
			}
			matches = append(matches, match{
				path:    path,
				line:    i + 1,
				score:   score,
				snippet: joinLines(block),
			})
		}
		return nil
	})

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			if matches[i].path == matches[j].path {
				return matches[i].line < matches[j].line
			}
			return matches[i].path < matches[j].path
		}
		return matches[i].score > matches[j].score
	})

	truncated := false
	if len(matches) > capMatches {
		matches = matches[:capMatches]
		truncated = true
	}

	blocks := make([]string, 0, len(matches)+1)
	for _, m := range matches {
		blocks = append(blocks, m.snippet)
	}
	if truncated {
		blocks = append(blocks, "truncated: additional matches omitted")
	}

	return t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   joinBlocks(blocks),
		Summary:   fmt.Sprintf("grep %q in %s", args.Pattern, root),
		Truncated: truncated,
	}), nil
}
