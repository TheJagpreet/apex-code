package diffengine

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func runHook(ctx context.Context, command, root string) VerificationResult {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-lc", command)
	}
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	result := VerificationResult{OK: err == nil, Command: command, Output: out.String()}
	if err == nil {
		return result
	}
	if exit, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exit.ExitCode()
	} else {
		result.ExitCode = -1
		if result.Output == "" {
			result.Output = err.Error()
		}
	}
	return result
}

func FilterFailure(output string, maxLines, maxChars int) string {
	if maxLines <= 0 {
		maxLines = 40
	}
	if maxChars <= 0 {
		maxChars = 4000
	}
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	selected := make([]string, 0, maxLines)
	seen := map[string]bool{}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		lower := strings.ToLower(trim)
		relevant := strings.Contains(lower, "error") ||
			strings.Contains(lower, "fail") ||
			strings.Contains(lower, "panic") ||
			strings.Contains(lower, "undefined") ||
			strings.Contains(lower, "cannot") ||
			strings.Contains(lower, ".go:") ||
			strings.Contains(lower, ".ts:") ||
			strings.Contains(lower, ".js:") ||
			strings.Contains(lower, ".py:")
		if !relevant {
			continue
		}
		key := trim
		if seen[key] {
			continue
		}
		seen[key] = true
		selected = append(selected, trim)
		if len(selected) >= maxLines {
			break
		}
	}
	if len(selected) == 0 {
		for _, line := range lines {
			trim := strings.TrimSpace(line)
			if trim == "" || seen[trim] {
				continue
			}
			selected = append(selected, trim)
			seen[trim] = true
			if len(selected) >= maxLines {
				break
			}
		}
	}
	text := strings.Join(selected, "\n")
	if len(text) > maxChars {
		text = text[:maxChars] + "\ntruncated: output capped at " + strconv.Itoa(maxChars) + " chars"
	}
	return text
}

func shellQuote(path string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(path, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}
