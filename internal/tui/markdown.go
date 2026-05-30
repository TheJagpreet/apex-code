package tui

import (
	"regexp"
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
func renderMarkdown(s string) string {
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

	for _, line := range lines {
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

		switch {
		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			out = append(out, styleMDRule.Render(strings.Repeat("─", 48)))
		case strings.HasPrefix(trimmed, "### "):
			out = append(out, styleMDH3.Render(strings.TrimPrefix(trimmed, "### ")))
		case strings.HasPrefix(trimmed, "## "):
			out = append(out, styleMDH2.Render(strings.TrimPrefix(trimmed, "## ")))
		case strings.HasPrefix(trimmed, "# "):
			out = append(out, styleMDH1.Render(strings.TrimPrefix(trimmed, "# ")))
		case strings.HasPrefix(trimmed, "> "):
			out = append(out, styleMDQuote.Render("│ "+renderInlineMarkdown(strings.TrimPrefix(trimmed, "> "))))
		case isBullet(trimmed):
			out = append(out, styleMDBullet.Render("  • ")+renderInlineMarkdown(trimmed[2:]))
		case reOrderedItem.MatchString(line):
			groups := reOrderedItem.FindStringSubmatch(line)
			out = append(out, styleMDBullet.Render("  "+groups[2]+". ")+renderInlineMarkdown(groups[3]))
		default:
			out = append(out, renderInlineMarkdown(line))
		}
	}
	flushCode()
	return strings.Join(out, "\n")
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
