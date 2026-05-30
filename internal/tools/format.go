package tools

import "strings"

func joinLines(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func joinBlocks(blocks []string) string {
	trimmed := make([]string, 0, len(blocks))
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block != "" {
			trimmed = append(trimmed, block)
		}
	}
	return strings.Join(trimmed, "\n\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
