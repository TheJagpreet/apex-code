package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type RunTool struct {
	gate Gate
}

type RunArgs struct {
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
}

func NewRunTool(gate Gate) Tool {
	return &RunTool{gate: gate}
}

func (t *RunTool) Name() string { return "run" }

func (t *RunTool) Description() string {
	return "Run a real local shell command. Best for git, node, tests, and other CLI tasks that are awkward with higher-level tools."
}

func (t *RunTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
			"dir":     map[string]any{"type": "string"},
		},
		"required": []string{"command"},
	})
}

func (t *RunTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 500
}

func (t *RunTool) Invoke(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args RunArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, fmt.Errorf("command must not be empty")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", args.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-lc", args.Command)
	}
	if args.Dir != "" {
		dir, err := resolvePath(args.Dir)
		if err != nil {
			return Result{}, err
		}
		cmd.Dir = dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return Result{}, err
		}
	}

	payload := strings.TrimSpace(joinBlocks([]string{
		fmt.Sprintf("exit_code: %d", exitCode),
		"stdout:\n" + limitLen(normalizeText(stdout.String()), defaultRunOutputChars),
		"stderr:\n" + limitLen(normalizeText(stderr.String()), defaultRunOutputChars),
	}))

	return t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   payload,
		Summary:   fmt.Sprintf("ran command exit=%d", exitCode),
		Truncated: len(stdout.String()) > defaultRunOutputChars || len(stderr.String()) > defaultRunOutputChars,
		IsError:   exitCode != 0,
	}), nil
}
