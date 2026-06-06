package telemetry

import (
	"strings"

	"github.com/apex-code/apex/internal/domain"
)

// ToolExecOutcome classifies a tool execution for telemetry without changing the
// tool result contract fed back to the model.
func ToolExecOutcome(results []domain.ToolResult) (outcome string, recoverable bool) {
	if len(results) == 0 {
		return "", false
	}
	hasError := false
	recoverableOnly := true
	for _, result := range results {
		if !result.IsError {
			continue
		}
		hasError = true
		if !isRecoverableToolError(result.Content) {
			recoverableOnly = false
		}
	}
	if !hasError {
		return "success", false
	}
	if recoverableOnly {
		return "recoverable_error", true
	}
	return "error", false
}

func isRecoverableToolError(content string) bool {
	text := strings.ToLower(strings.TrimSpace(content))
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"provide start_line and end_line",
		"invalid line range",
		"missing required",
		"invalid arguments",
		"schema validation",
		"expected object",
		"path is a directory",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
