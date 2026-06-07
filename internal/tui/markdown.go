package tui

import (
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Markdown styling. Kept intentionally compact so agent replies read like
// rendered markdown in the terminal instead of raw `#`, `**`, and backtick
// noise.
var (
	styleMDH1        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	styleMDH2        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleMDH3        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	styleMDBold      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	styleMDItalic    = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("252"))
	styleMDCode      = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Background(lipgloss.Color("236")).Padding(0, 1)
	styleMDCodeBlock = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236"))
	styleMDBullet    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	styleMDQuote     = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("244"))
	styleMDRule      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleMDTableHdr  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230"))
	styleMDTableRule = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

var (
	reBold        = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reBoldUnder   = regexp.MustCompile(`__([^_]+)__`)
	reItalicStar  = regexp.MustCompile(`\*([^*]+)\*`)
	reOrderedItem = regexp.MustCompile(`^(\s*)(\d+)\.\s+(.*)$`)
)

// renderMarkdown converts a subset of Markdown (headings, lists, blockquotes,
// fenced/inline code, bold, italic, and horizontal rules) into styled terminal
// text. It is deliberately forgiving: anything it does not recognize is passed
// through with inline emphasis applied.
func renderMarkdown(s string, width int) string {
	if width < 24 {
		width = 24
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	var code []string

	flushCode := func() {
		if len(code) > 0 {
			out = append(out, renderCodeBlock(code))
			code = nil
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				flushCode()
				inFence = false
			} else {
				inFence = true
			}
			continue
		}
		if inFence {
			code = append(code, line)
			continue
		}
		if header, rows, ok := parseMarkdownTable(lines, i); ok {
			out = append(out, renderMarkdownTable(header, rows, width))
			i += len(rows) + 1
			continue
		}

		switch {
		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			out = append(out, styleMDRule.Render(strings.Repeat("─", 48)))
		case strings.HasPrefix(trimmed, "### "):
			out = append(out, renderWrappedStyledText(strings.TrimPrefix(trimmed, "### "), width, styleMDH3)...)
		case strings.HasPrefix(trimmed, "## "):
			out = append(out, renderWrappedStyledText(strings.TrimPrefix(trimmed, "## "), width, styleMDH2)...)
		case strings.HasPrefix(trimmed, "# "):
			out = append(out, renderWrappedStyledText(strings.TrimPrefix(trimmed, "# "), width, styleMDH1)...)
		case strings.HasPrefix(trimmed, "> "):
			out = append(out, renderWrappedPrefixedMarkdown(strings.TrimPrefix(trimmed, "> "), width, "│ ", styleMDQuote)...)
		case isBullet(trimmed):
			out = append(out, renderWrappedPrefixedMarkdown(trimmed[2:], width, "  • ", styleMDBullet)...)
		case reOrderedItem.MatchString(line):
			groups := reOrderedItem.FindStringSubmatch(line)
			out = append(out, renderWrappedPrefixedMarkdown(groups[3], width, "  "+groups[2]+". ", styleMDBullet)...)
		default:
			out = append(out, renderWrappedInlineMarkdown(line, width)...)
		}
	}
	flushCode()
	return strings.Join(out, "\n")
}

func parseMarkdownTable(lines []string, start int) ([]string, [][]string, bool) {
	if start+1 >= len(lines) {
		return nil, nil, false
	}
	header := parseTableRow(lines[start])
	if len(header) == 0 {
		return nil, nil, false
	}
	if !isTableSeparator(lines[start+1], len(header)) {
		return nil, nil, false
	}
	var rows [][]string
	for i := start + 2; i < len(lines); i++ {
		row := parseTableRow(lines[i])
		if len(row) == 0 {
			break
		}
		rows = append(rows, normalizeTableCells(row, len(header)))
	}
	return normalizeTableCells(header, len(header)), rows, true
}

func parseTableRow(line string) []string {
	if strings.Count(line, "|") < 2 {
		return nil
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "|") {
		trimmed = strings.TrimPrefix(trimmed, "|")
	}
	if strings.HasSuffix(trimmed, "|") {
		trimmed = strings.TrimSuffix(trimmed, "|")
	}
	parts := strings.Split(trimmed, "|")
	if len(parts) < 2 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func isTableSeparator(line string, cols int) bool {
	cells := parseTableRow(line)
	if len(cells) != cols {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		for _, r := range cell {
			if r != '-' && r != ':' {
				return false
			}
		}
	}
	return true
}

func normalizeTableCells(cells []string, cols int) []string {
	if len(cells) == cols {
		return cells
	}
	out := make([]string, cols)
	copy(out, cells)
	return out
}

func renderMarkdownTable(header []string, rows [][]string, width int) string {
	cols := len(header)
	if cols == 0 {
		return ""
	}
	widths := make([]int, cols)
	allRows := make([][]string, 0, len(rows)+1)
	allRows = append(allRows, header)
	allRows = append(allRows, rows...)
	for _, row := range allRows {
		for i := 0; i < cols; i++ {
			widths[i] = max(widths[i], lipgloss.Width(strings.TrimSpace(row[i])))
		}
	}
	widths = fitTableWidths(widths, width-(cols*3)-1)
	var out []string
	out = append(out, tableBorder("┌", "┬", "┐", widths))
	out = append(out, renderTableRow(header, widths, true)...)
	out = append(out, tableBorder("├", "┼", "┤", widths))
	for i, row := range rows {
		out = append(out, renderTableRow(row, widths, false)...)
		if i < len(rows)-1 {
			out = append(out, tableBorder("├", "┼", "┤", widths))
		}
	}
	out = append(out, tableBorder("└", "┴", "┘", widths))
	return strings.Join(out, "\n")
}

func fitTableWidths(widths []int, target int) []int {
	out := slices.Clone(widths)
	if target < len(out)*6 {
		target = len(out) * 6
	}
	total := 0
	for _, w := range out {
		if w < 6 {
			w = 6
		}
		total += w
	}
	for i := range out {
		if out[i] < 6 {
			out[i] = 6
		}
	}
	for total > target {
		maxIdx := 0
		for i := 1; i < len(out); i++ {
			if out[i] > out[maxIdx] {
				maxIdx = i
			}
		}
		if out[maxIdx] <= 6 {
			break
		}
		out[maxIdx]--
		total--
	}
	return out
}

func tableBorder(left, mid, right string, widths []int) string {
	var b strings.Builder
	b.WriteString(styleMDTableRule.Render(left))
	for i, w := range widths {
		b.WriteString(styleMDTableRule.Render(strings.Repeat("─", w+2)))
		if i < len(widths)-1 {
			b.WriteString(styleMDTableRule.Render(mid))
		}
	}
	b.WriteString(styleMDTableRule.Render(right))
	return b.String()
}

func renderTableRow(row []string, widths []int, header bool) []string {
	cells := make([][]string, len(widths))
	rowHeight := 1
	for i := range widths {
		lines := wrapPlainText(strings.TrimSpace(row[i]), widths[i])
		if len(lines) == 0 {
			lines = []string{""}
		}
		cells[i] = lines
		if len(lines) > rowHeight {
			rowHeight = len(lines)
		}
	}
	out := make([]string, 0, rowHeight)
	for lineIdx := 0; lineIdx < rowHeight; lineIdx++ {
		var b strings.Builder
		b.WriteString(styleMDTableRule.Render("│"))
		for colIdx, cellLines := range cells {
			text := ""
			if lineIdx < len(cellLines) {
				text = cellLines[lineIdx]
			}
			rendered := renderInlineMarkdown(text)
			if header {
				rendered = styleMDTableHdr.Render(text)
			}
			b.WriteString(" ")
			b.WriteString(padStyled(rendered, widths[colIdx]))
			b.WriteString(" ")
			b.WriteString(styleMDTableRule.Render("│"))
		}
		out = append(out, b.String())
	}
	return out
}

func wrapPlainText(s string, width int) []string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
	if s == "" {
		return nil
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			candidate := line + " " + word
			if lipgloss.Width(candidate) <= width {
				line = candidate
				continue
			}
			out = append(out, line)
			line = word
			for lipgloss.Width(line) > width {
				cut := width
				if cut < 1 {
					cut = 1
				}
				out = append(out, line[:cut])
				line = line[cut:]
			}
		}
		out = append(out, line)
	}
	return out
}

func padStyled(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

func renderWrappedInlineMarkdown(s string, width int) []string {
	lines := wrapPlainTextPreserveIndent(s, width)
	if len(lines) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderInlineMarkdown(line))
	}
	return out
}

func renderWrappedStyledText(s string, width int, style lipgloss.Style) []string {
	lines := wrapPlainTextPreserveIndent(s, width)
	if len(lines) == 0 {
		return []string{style.Render("")}
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, style.Render(line))
	}
	return out
}

func renderWrappedPrefixedMarkdown(s string, width int, prefix string, prefixStyle lipgloss.Style) []string {
	prefixWidth := lipgloss.Width(prefix)
	bodyWidth := width - prefixWidth
	if bodyWidth < 8 {
		bodyWidth = 8
	}
	lines := wrapPlainTextPreserveIndent(s, bodyWidth)
	if len(lines) == 0 {
		lines = []string{""}
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		currentPrefix := strings.Repeat(" ", prefixWidth)
		if i == 0 {
			currentPrefix = prefix
		}
		out = append(out, prefixStyle.Render(currentPrefix)+renderInlineMarkdown(line))
	}
	return out
}

func wrapPlainTextPreserveIndent(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	s = strings.ReplaceAll(s, "\r", "")
	if strings.TrimSpace(s) == "" {
		return []string{""}
	}
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		if strings.TrimSpace(raw) == "" {
			out = append(out, "")
			continue
		}
		indentLen := len(raw) - len(strings.TrimLeft(raw, " "))
		indent := raw[:indentLen]
		trimmed := strings.TrimSpace(raw)
		bodyWidth := width - lipgloss.Width(indent)
		if bodyWidth < 8 {
			bodyWidth = width
			indent = ""
		}
		wrapped := wrapPlainText(trimmed, bodyWidth)
		if len(wrapped) == 0 {
			out = append(out, indent)
			continue
		}
		for _, line := range wrapped {
			out = append(out, indent+line)
		}
	}
	return out
}

func isBullet(trimmed string) bool {
	return strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ")
}

func renderCodeBlock(lines []string) string {
	rendered := make([]string, len(lines))
	for i, l := range lines {
		rendered[i] = styleMDCodeBlock.Render("  " + l + " ")
	}
	return strings.Join(rendered, "\n")
}

// renderInlineMarkdown applies inline code spans and emphasis. Inline code is
// extracted first so its contents are never reinterpreted as emphasis.
func renderInlineMarkdown(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			b.WriteString(applyEmphasis(s))
			break
		}
		j := strings.IndexByte(s[i+1:], '`')
		if j < 0 {
			b.WriteString(applyEmphasis(s))
			break
		}
		b.WriteString(applyEmphasis(s[:i]))
		b.WriteString(styleMDCode.Render(s[i+1 : i+1+j]))
		s = s[i+1+j+1:]
	}
	return b.String()
}

func applyEmphasis(s string) string {
	// Bold before italic so "**x**" is not consumed by the single-star rule.
	s = reBold.ReplaceAllString(s, styleMDBold.Render("$1"))
	s = reBoldUnder.ReplaceAllString(s, styleMDBold.Render("$1"))
	s = reItalicStar.ReplaceAllString(s, styleMDItalic.Render("$1"))
	return s
}
