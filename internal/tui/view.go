// Package tui implements apex-code's interactive Bubble Tea interface: a
// polished terminal workspace with a branded landing state, structured
// transcript, slash-command composer, @file references, token stats, and
// inspection panes for tools, diffs, and context.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Agent is the minimal surface the TUI needs from the agent layer. cli supplies
// the concrete implementation so the TUI stays decoupled from wiring.
type Agent interface {
	Send(ctx context.Context, prompt string) (Reply, error)
	// Stream runs one turn while delivering assistant text deltas to onDelta as
	// they arrive, returning the same final Reply that Send would. Implementations
	// may call onDelta from another goroutine.
	Stream(ctx context.Context, prompt string, onDelta func(string)) (Reply, error)
	Model() string
	CWD() string
	SessionLabel() string
	LazyTools() bool
	ResumeSession(ctx context.Context, selector string) error
	NewSession() error
	SetModel(ctx context.Context, model string) error
	ListSessions(ctx context.Context, limit int) ([]SessionOption, error)
}

// Reply is one agent response rendered into the TUI.
type Reply struct {
	Text        string
	Turns       int
	Termination string
	Budget      BudgetSnapshot
	ToolCalls   []ToolCallView
	Diffs       []DiffView
	Stats       string
}

// SessionOption is a compact session listing shown by the TUI.
type SessionOption struct {
	ID      string
	Model   string
	Title   string
	Summary string
}

// BudgetSnapshot is the token accounting surfaced in the status line.
type BudgetSnapshot struct {
	PromptTokens   int
	PromptLimit    int
	OutputHeadroom int
	Pools          []PoolSnapshot
}

// PoolSnapshot is one named budget pool's usage.
type PoolSnapshot struct {
	Name   string
	Tokens int
	Limit  int
}

// ToolCallView is one tool invocation shown in the inspector pane.
type ToolCallView struct {
	Name string
	Args string
}

// DiffView is a proposed edit shown in the diff viewer.
type DiffView struct {
	File  string
	Patch string
}

type entryKind string

const (
	entryStatus    entryKind = "status"
	entryUser      entryKind = "user"
	entryAssistant entryKind = "assistant"
	entryError     entryKind = "error"
)

type transcriptEntry struct {
	Kind    entryKind
	Title   string
	Body    string
	Meta    string
	Compact bool
}

func (e transcriptEntry) rawText() string {
	title := strings.TrimSpace(e.Title)
	body := strings.TrimSpace(e.Body)
	switch {
	case title == "":
		return body
	case body == "":
		return title
	default:
		return title + "\n" + body
	}
}

type suggestionKind string

const (
	suggestionCommand suggestionKind = "command"
	suggestionFile    suggestionKind = "file"
	suggestionPrompt  suggestionKind = "prompt"
)

type suggestion struct {
	Label   string
	Insert  string
	Detail  string
	Kind    suggestionKind
	Replace string
}

type commandSpec struct {
	Name        string
	Usage       string
	Description string
}

type promptSpec struct {
	Name        string
	Body        string
	Description string
}

var slashCommands = []commandSpec{
	{Name: "/help", Usage: "/help", Description: "show the command reference"},
	{Name: "/pane", Usage: "/pane [chat|tools|diffs|context|stats|help]", Description: "switch the right-hand pane"},
	{Name: "/pin", Usage: "/pin <file> [more files]", Description: "pin files into the visible working set list"},
	{Name: "/unpin", Usage: "/unpin <file> [more files]", Description: "remove pinned files"},
	{Name: "/explain", Usage: "/explain", Description: "insert the repo walkthrough starter"},
	{Name: "/review", Usage: "/review", Description: "insert the code review starter"},
	{Name: "/fix", Usage: "/fix", Description: "insert the debug and fix starter"},
	{Name: "/test", Usage: "/test", Description: "insert the testing starter"},
	{Name: "/model", Usage: "/model [name]", Description: "show or switch the active model"},
	{Name: "/resume", Usage: "/resume [id]", Description: "list sessions or resume a selected saved session"},
	{Name: "/sessions", Usage: "/sessions", Description: "list recent sessions"},
	{Name: "/new", Usage: "/new", Description: "start a clean session"},
	{Name: "/stats", Usage: "/stats", Description: "focus the stats pane"},
	{Name: "/prompts", Usage: "/prompts", Description: "list built-in prompt starters"},
	{Name: "/companion", Usage: "/companion", Description: "switch the footer companion"},
	{Name: "/theme", Usage: "/theme [name]", Description: "cycle or set the color theme"},
	{Name: "/verbose", Usage: "/verbose", Description: "toggle expanded technical details"},
	{Name: "/clear", Usage: "/clear", Description: "clear the visible transcript"},
	{Name: "/quit", Usage: "/quit", Description: "exit apex-code"},
}

var starterPrompts = []promptSpec{
	{Name: "explain", Body: "Explain the architecture and key data flow of this repository.", Description: "repo walkthrough"},
	{Name: "review", Body: "Review the recent changes for bugs, regressions, and missing tests.", Description: "code review"},
	{Name: "fix", Body: "Investigate the failing behavior, propose the smallest safe fix, implement it, and verify it.", Description: "debug and fix"},
	{Name: "test", Body: "Add or tighten tests around the affected behavior and explain what the tests now cover.", Description: "testing pass"},
}

const apexASCII = `
      /\                 
     /  \   apex-code    
    / /\ \  local-first  
   / ____ \ coding agent 
  /_/    \_\             
`

var loaderGlyphs = []string{"A", "/", "-", `\`}

var loaderPhrases = []string{
	"mapping the repo maze",
	"counting tokens like a hawk",
	"lining up tools and patches",
	"peeking at files without wasting context",
	"nudging the model toward the smallest safe move",
}

// renderBudgetMeter draws the live token-budget meter. width is the number of
// bar cells. Pure so it can be unit-tested.
func renderBudgetMeter(b BudgetSnapshot, width int, verbose bool) string {
	if width < 4 {
		width = 4
	}
	limit := b.PromptLimit
	used := b.PromptTokens
	pct := 0.0
	if limit > 0 {
		pct = float64(used) / float64(limit)
	}
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}

	fillStyle := styleMeterFill
	if limit > 0 && used > limit {
		fillStyle = styleMeterOver
	}
	bar := fillStyle.Render(strings.Repeat("#", filled)) +
		styleMeterEmpty.Render(strings.Repeat("-", width-filled))

	label := "context"
	if verbose {
		label = "budget"
	}
	out := fmt.Sprintf("%s [%s] %d/%d tok (%d%%)", label, bar, used, limit, int(pct*100))
	if verbose {
		out += fmt.Sprintf("  headroom=%d", b.OutputHeadroom)
	}
	return out
}

// renderPools renders the per-pool breakdown line.
func renderPools(b BudgetSnapshot, verbose bool) string {
	if !verbose {
		return ""
	}
	if len(b.Pools) == 0 {
		return ""
	}
	parts := make([]string, 0, len(b.Pools))
	for _, p := range b.Pools {
		parts = append(parts, fmt.Sprintf("%s %d/%d", p.Name, p.Tokens, p.Limit))
	}
	return styleDim.Render("pools: " + strings.Join(parts, " · "))
}

func renderWelcome(status runtimeStatus) string {
	var b strings.Builder
	b.WriteString(styleBanner.Render(strings.TrimSpace(apexASCII)))
	b.WriteString("\n")
	b.WriteString(styleHeader.Render("A compact coding workspace for daily use"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render("Start typing, mention files with @, or reach for /help and /prompts."))
	b.WriteString("\n\n")
	b.WriteString(renderBadgeRow(status))
	return b.String()
}

// renderHeaderCompact is a two-line header shown once a conversation exists, so
// the full landing banner doesn't crowd out the scrollable chat.
func renderHeaderCompact(status runtimeStatus) string {
	title := styleBanner.Render("apex-code") + "  " + styleDim.Render("local-first coding agent")
	return title + "\n" + renderBadgeRow(status)
}

func renderTranscript(entries []transcriptEntry, windowSize int, scrollOffset int, verbose bool) string {
	if len(entries) == 0 {
		return ""
	}
	if windowSize < 1 {
		windowSize = 1
	}
	end := len(entries) - scrollOffset
	if end < 0 {
		end = 0
	}
	start := end - windowSize
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	parts := make([]string, 0, end-start+2)
	if start > 0 {
		parts = append(parts, styleDim.Render(fmt.Sprintf("[older messages above: %d]", start)))
	}
	for _, entry := range entries[start:end] {
		parts = append(parts, renderEntry(entry, false, verbose))
	}
	if end < len(entries) {
		parts = append(parts, styleDim.Render(fmt.Sprintf("[newer messages below: %d]", len(entries)-end)))
	}
	return strings.Join(parts, "\n\n")
}

// renderAllEntries renders every transcript entry into one block. The View
// layer slices this into a height-bounded, line-scrollable viewport, so this
// function intentionally renders the full conversation.
func renderAllEntries(entries []transcriptEntry, selected int, verbose bool) string {
	parts := make([]string, 0, len(entries))
	for i, entry := range entries {
		parts = append(parts, renderEntry(entry, i == selected, verbose))
	}
	return strings.Join(parts, "\n\n")
}

// renderScrollbar draws a vertical scrollbar of the given height. scrollOffset
// counts entries hidden below the window, so offset 0 parks the thumb at the
// bottom (newest) and the maximum offset parks it at the top (oldest).
func renderScrollbar(total, window, scrollOffset, height int) string {
	if height < 1 {
		height = 1
	}
	if total <= window {
		return strings.TrimRight(strings.Repeat(styleScrollTrack.Render("│")+"\n", height), "\n")
	}
	maxOffset := total - window
	thumb := height * window / total
	if thumb < 1 {
		thumb = 1
	}
	if thumb > height {
		thumb = height
	}
	travel := height - thumb
	// Fraction scrolled from the top: full at maxOffset, zero at the bottom.
	top := 0
	if maxOffset > 0 {
		top = travel * (maxOffset - scrollOffset) / maxOffset
	}
	rows := make([]string, height)
	for i := range rows {
		if i >= top && i < top+thumb {
			rows[i] = styleScrollThumb.Render("█")
		} else {
			rows[i] = styleScrollTrack.Render("│")
		}
	}
	return strings.Join(rows, "\n")
}

func renderEntry(entry transcriptEntry, selected bool, verbose bool) string {
	title := entry.Title
	if title == "" {
		title = strings.Title(string(entry.Kind))
	}
	body := strings.TrimSpace(entry.Body)
	if body == "" {
		body = "(empty)"
	}
	switch entry.Kind {
	case entryUser:
		body = renderUserPrompt(body)
	case entryAssistant:
		body = renderMarkdown(body)
	}
	lineWidth := 72
	ruleStyle := styleDim
	titleStyle := styleHeader
	switch entry.Kind {
	case entryAssistant:
		titleStyle = stylePaneTitle
	case entryError:
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
		ruleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	}
	block := ruleStyle.Render(strings.Repeat("─", lineWidth))
	if selected {
		title += "  " + styleDim.Render("[ctrl+y copy]")
	}
	block += "\n" + titleStyle.Render(title)
	if verbose && entry.Meta != "" {
		block += "\n" + styleDim.Render(entry.Meta)
	}
	block += "\n" + body
	block += "\n" + ruleStyle.Render(strings.Repeat("─", lineWidth))
	if selected {
		return styleEntrySelected.Render(block)
	}
	return block
}

func renderBadgeRow(status runtimeStatus) string {
	badges := []string{
		styleBadgeOn.Render("model " + status.Model),
		styleBadgeOn.Render("companion " + status.Companion),
		styleBadgeOn.Render("theme " + status.Theme),
		styleBadgeOff.Render("cwd " + status.CWD),
		styleBadgeOff.Render("session " + status.Session),
	}
	if status.LazyTools {
		badges = append(badges, styleBadgeOn.Render("lazy-tools"))
	} else {
		badges = append(badges, styleBadgeOff.Render("eager-tools"))
	}
	return strings.Join(badges, " ")
}

func renderToolInspector(calls []ToolCallView) string {
	if len(calls) == 0 {
		return styleDim.Render("(no tool calls yet)")
	}
	var b strings.Builder
	b.WriteString(stylePaneTitle.Render("Tool Activity"))
	b.WriteString("\n")
	for i, c := range calls {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c.Name)
		b.WriteString(styleDim.Render("   " + oneLine(c.Args)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderDiff(d DiffView) string {
	var b strings.Builder
	b.WriteString(stylePaneTitle.Render("edit " + d.File))
	b.WriteString("\n")
	for _, line := range strings.Split(d.Patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			b.WriteString(styleAdd.Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(styleDel.Render(line))
		default:
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderDiffs(diffs []DiffView) string {
	if len(diffs) == 0 {
		return styleDim.Render("(no proposed edits)")
	}
	parts := make([]string, 0, len(diffs))
	for _, d := range diffs {
		parts = append(parts, renderDiff(d))
	}
	return strings.Join(parts, "\n\n")
}

func renderContextPane(pins []string, refs []string) string {
	var b strings.Builder
	b.WriteString(stylePaneTitle.Render("Context"))
	b.WriteString("\n")
	if len(pins) == 0 {
		b.WriteString(styleDim.Render("Pinned files: none"))
	} else {
		b.WriteString("Pinned files:\n")
		for _, pin := range pins {
			b.WriteString("- " + pin + "\n")
		}
	}
	b.WriteString("\n")
	if len(refs) == 0 {
		b.WriteString(styleDim.Render("Recent @ references: none"))
	} else {
		b.WriteString("Recent @ references:\n")
		for _, ref := range refs {
			b.WriteString("- @" + ref + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderStatsPane(budget BudgetSnapshot, stats string, verbose bool) string {
	var b strings.Builder
	b.WriteString(stylePaneTitle.Render("Stats"))
	b.WriteString("\n")
	b.WriteString(renderBudgetMeter(budget, 22, verbose))
	if pools := renderPools(budget, verbose); pools != "" {
		b.WriteString("\n")
		b.WriteString(pools)
	}
	if verbose && strings.TrimSpace(stats) != "" {
		b.WriteString("\n\n")
		b.WriteString(stats)
	}
	return b.String()
}

func renderHelpPane() string {
	var b strings.Builder
	b.WriteString(styleHelpHeader.Render("Slash Commands"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render("Command          What it does"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render(strings.Repeat("─", 64)))
	b.WriteString("\n")
	for _, cmd := range slashCommands {
		left := padRight(cmd.Usage, 18)
		b.WriteString(styleHelpCmd.Render(left))
		b.WriteString("  ")
		b.WriteString(styleHelpUsage.Render(cmd.Description))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleHelpHeader.Render("Prompt Starters"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render("Starter           Purpose"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render(strings.Repeat("─", 64)))
	b.WriteString("\n")
	for _, prompt := range starterPrompts {
		left := padRight("/"+prompt.Name, 18)
		b.WriteString(styleHelpCmd.Render(left))
		b.WriteString("  ")
		b.WriteString(styleHelpUsage.Render(prompt.Description))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderComposer(input string, suggestions []suggestion, idx int, pane Pane, width int, verbose bool, copyStatus string) string {
	var b strings.Builder
	rule := strings.Repeat("─", composerWidth(width))
	b.WriteString(styleDim.Render(rule))
	b.WriteString("\n")
	b.WriteString(input + "▌")
	b.WriteString("\n")
	b.WriteString(styleDim.Render(rule))
	if len(suggestions) > 0 {
		b.WriteString("\n")
		for i, s := range suggestions {
			line := fmt.Sprintf("%s  %s", s.Label, styleMuted.Render(s.Detail))
			if i == idx {
				b.WriteString(styleSuggestionSel.Render(line))
			} else {
				b.WriteString(styleSuggestion.Render(line))
			}
			if i < len(suggestions)-1 {
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
	helpLine := "[enter] send  [ctrl+j] newline  [tab] accept  [up/down] select/history  [ctrl+y] copy message  [wheel/pgup/pgdn] scroll  [ctrl+c] cancel/quit  [esc] quit"
	if verbose {
		helpLine = fmt.Sprintf("%s  [ctrl+o] pane(%s)", helpLine, pane)
	}
	b.WriteString(styleDim.Render(helpLine))
	if strings.TrimSpace(copyStatus) != "" {
		b.WriteString("\n")
		b.WriteString(stylePaneTitle.Render(copyStatus))
	}
	return b.String()
}

func renderStatusFooter(status runtimeStatus, pane Pane) string {
	return styleDim.Render(fmt.Sprintf("provider=ollama  model=%s  session=%s  pane=%s  [f2] companion  [f3] theme", status.Model, status.Session, pane))
}

func renderStatsStrip(budget BudgetSnapshot, stats string, width int, verbose bool) string {
	var b strings.Builder
	b.WriteString(stylePaneTitle.Render("Live Stats"))
	b.WriteString("\n")
	b.WriteString(renderBudgetMeter(budget, meterWidth(width), verbose))
	if pools := renderPools(budget, verbose); pools != "" {
		b.WriteString("\n")
		b.WriteString(pools)
	}
	if verbose && strings.TrimSpace(stats) != "" {
		b.WriteString("\n")
		b.WriteString(styleDim.Render(stats))
	}
	return b.String()
}

func renderLoader(frame, phrase int) string {
	glyph := loaderGlyphs[frame%len(loaderGlyphs)]
	text := loaderPhrases[phrase%len(loaderPhrases)]
	return stylePaneTitle.Render("["+glyph+"] ") + styleDim.Render(text)
}

func renderAppFrame(content string, width int) string {
	frameWidth := width - 2
	if frameWidth < 70 {
		frameWidth = 70
	}
	return styleAppFrame.Width(frameWidth).Render(content)
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 96 {
		s = s[:96] + " …"
	}
	return s
}

func renderUserPrompt(body string) string {
	refs := extractRefs(body)
	for _, ref := range refs {
		body = strings.ReplaceAll(body, "@"+ref, styleRef.Render(ref))
	}
	return body
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func composerWidth(termWidth int) int {
	w := termWidth - 2
	if w < 24 {
		return 24
	}
	if w > 140 {
		return 140
	}
	return w
}
