// Package tui implements apex-code's interactive Bubble Tea interface: a
// polished terminal workspace with a branded landing state, structured
// transcript, slash-command composer, @file references, token stats, and
// inspection panes for tools, diffs, and context.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/apex-code/apex/internal/domain"
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
	Mode() string
	SetMode(ctx context.Context, mode string) error
	CoderSubmit(ctx context.Context, prompt string) (Reply, error)
	CoderReview(ctx context.Context, feedback string) (Reply, error)
	CoderApprove(ctx context.Context) (Reply, error)
	CoderExecute(ctx context.Context) (Reply, error)
	CoderExecuteStream(ctx context.Context, onUpdate func(Reply)) (Reply, error)
	CoderWorkflow() *domain.CoderWorkflow
	LiveStatus(ctx context.Context) (Reply, error)
	Extensions() ExtensionView
	ReloadExtensions(ctx context.Context) (ExtensionView, error)
	SetActiveAgent(ctx context.Context, name string) error
	ActivateSkill(ctx context.Context, name string) error
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
	Mode        string
	Workflow    *domain.CoderWorkflow
	ProgressKind      string
	ProgressAgent     string
	ProgressTaskTitle string
	ProgressSummary   string
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
	SessionTokens  int
	SessionTracked bool
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

type BundleHeaderView struct {
	Name        string
	Description string
	File        string
	Aliases     []string
	Skills      []string
}

type ExtensionView struct {
	AvailableAgents  []BundleHeaderView
	AvailableSkills  []BundleHeaderView
	ActiveAgent      string
	ActiveAgentFile  string
	ActiveSkills     []string
	ActiveSkillFiles []string
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
	{Name: "/agents", Usage: "/agents", Description: "list discovered custom agents"},
	{Name: "/agent", Usage: "/agent [name|clear]", Description: "set or clear the active custom agent"},
	{Name: "/skills", Usage: "/skills", Description: "list discovered and loaded custom skills"},
	{Name: "/reload", Usage: "/reload", Description: "reload custom agents and skills from disk"},
	{Name: "/explain", Usage: "/explain", Description: "insert the repo walkthrough starter"},
	{Name: "/review", Usage: "/review", Description: "insert the code review starter"},
	{Name: "/fix", Usage: "/fix", Description: "insert the debug and fix starter"},
	{Name: "/test", Usage: "/test", Description: "insert the testing starter"},
	{Name: "/chat", Usage: "/chat", Description: "switch to normal chat mode"},
	{Name: "/coder", Usage: "/coder", Description: "switch to coder workflow mode"},
	{Name: "/plan", Usage: "/plan", Description: "show the current coder workflow plan"},
	{Name: "/approve", Usage: "/approve", Description: "approve the current coder plan and start execution"},
	{Name: "/replan", Usage: "/replan <feedback>", Description: "revise the current coder plan"},
	{Name: "/resume", Usage: "/resume [id]", Description: "list sessions or resume a selected saved session"},
	{Name: "/sessions", Usage: "/sessions", Description: "list recent sessions"},
	{Name: "/new", Usage: "/new", Description: "start a clean session"},
	{Name: "/companion", Usage: "/companion", Description: "switch the footer companion"},
	{Name: "/theme", Usage: "/theme [name]", Description: "cycle or set the color theme"},
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
	if b.PromptLimit <= 0 {
		return styleDim.Render(fmt.Sprintf("context %d/infinity tok", b.PromptTokens))
	}
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

func renderBudgetCompact(b BudgetSnapshot) string {
	if b.SessionTracked {
		return fmt.Sprintf("tok %d", b.SessionTokens)
	}
	return fmt.Sprintf("tok %d", b.PromptTokens)
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
	b.WriteString(styleBanner.Render(strings.Trim(apexASCII, "\n")))
	b.WriteString("\n")
	b.WriteString(styleHeader.Render("A compact coding workspace for daily use"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render("Start typing, mention files with @, or reach for /help and /prompts."))
	b.WriteString("\n")
	b.WriteString(styleDim.Render(fmt.Sprintf("Discovered %d custom agent file(s) and %d custom skill file(s).", status.AgentCount, status.SkillCount)))
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

func renderTranscript(entries []transcriptEntry, windowSize int, scrollOffset int, verbose bool, width int) string {
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
		parts = append(parts, renderEntry(entry, false, verbose, width))
	}
	if end < len(entries) {
		parts = append(parts, styleDim.Render(fmt.Sprintf("[newer messages below: %d]", len(entries)-end)))
	}
	return strings.Join(parts, "\n\n")
}

// renderAllEntries renders every transcript entry into one block. The View
// layer slices this into a height-bounded, line-scrollable viewport, so this
// function intentionally renders the full conversation.
func renderAllEntries(entries []transcriptEntry, selected int, verbose bool, width int) string {
	parts := make([]string, 0, len(entries))
	for i, entry := range entries {
		parts = append(parts, renderEntry(entry, i == selected, verbose, width))
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

func renderEntry(entry transcriptEntry, selected bool, verbose bool, width int) string {
	if width < 8 {
		width = 8
	}
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
		body = wrapTo(renderUserPrompt(body), width)
	case entryAssistant:
		body = renderMarkdown(body, width)
	default:
		body = wrapTo(body, width)
	}
	titleStyle := styleHeader
	switch entry.Kind {
	case entryAssistant:
		titleStyle = stylePaneTitle
	case entryError:
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	}
	block := ""
	if selected {
		title += "  " + styleDim.Render("[ctrl+y copy]")
	}
	block += wrapTo(titleStyle.Render(title), width)
	if verbose && entry.Meta != "" {
		block += "\n" + wrapTo(styleDim.Render(entry.Meta), width)
	}
	block += "\n" + body
	if selected {
		return styleEntrySelected.Render(block)
	}
	return block
}

func renderBadgeRow(status runtimeStatus) string {
	badges := []string{
		styleBadgeOff.Render(compactSessionLabel(status.Session)),
		styleBadgeOn.Render("mode " + status.Mode),
		styleBadgeOn.Render("companion " + status.Companion),
		styleBadgeOff.Render(status.ContextSummary),
		styleBadgeOff.Render("cwd " + status.CWD),
		styleBadgeOff.Render(fmt.Sprintf("agents %d", status.AgentCount)),
		styleBadgeOff.Render(fmt.Sprintf("skills %d", status.SkillCount)),
	}
	if strings.TrimSpace(status.ActiveAgent) != "" {
		badges = append(badges, styleBadgeOn.Render(compactBundleLabel(status.ActiveAgent)))
	}
	if len(status.ActiveSkills) > 0 {
		badges = append(badges, styleBadgeOff.Render("skills "+compactBundleList(status.ActiveSkills)))
	}
	return strings.Join(badges, " ")
}

func compactSessionLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "new"
	}
	if len(label) <= 8 {
		return label
	}
	return label[:8] + "..."
}

func compactBundleLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "-"
	}
	if len(label) <= 18 {
		return label
	}
	return label[:15] + "..."
}

func compactBundleList(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	if len(items) == 1 {
		return compactBundleLabel(items[0])
	}
	return compactBundleLabel(items[0]) + fmt.Sprintf("+%d", len(items)-1)
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

func renderComposer(input string, cursorPos int, suggestions []suggestion, idx int, pane Pane, width int, verbose bool, copyStatus string, working bool) string {
	var b strings.Builder
	rule := strings.Repeat("─", composerWidth(width))
	ruleStyle := styleComposerIdle
	if working {
		ruleStyle = styleComposerBusy
	}
	b.WriteString(ruleStyle.Render(rule))
	b.WriteString("\n")
	cursor := "▌"
	if working {
		cursor = ""
	}
	runes := []rune(input)
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(runes) {
		cursorPos = len(runes)
	}
	line := string(runes[:cursorPos]) + cursor + string(runes[cursorPos:])
	b.WriteString(line)
	b.WriteString("\n")
	b.WriteString(ruleStyle.Render(rule))
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
	helpLine := "[ctrl+c] cancel/quit  [esc] quit  [alt+p] companion  [alt+t] theme"
	if verbose {
		helpLine = fmt.Sprintf("%s  [ctrl+y] copy  pane(%s)", helpLine, pane)
	}
	b.WriteString(styleDim.Render(helpLine))
	if strings.TrimSpace(copyStatus) != "" {
		b.WriteString("\n")
		b.WriteString(stylePaneTitle.Render(copyStatus))
	}
	return b.String()
}

func renderStatusFooter(status runtimeStatus, pane Pane) string {
	return ""
}

func renderPlanPane(wf *domain.CoderWorkflow) string {
	if wf == nil {
		return styleDim.Render("(no coder workflow yet)")
	}
	var b strings.Builder
	b.WriteString(stylePaneTitle.Render("Coder Workflow"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render(fmt.Sprintf("workflow=%s  state=%s  plan_version=%d  active_agent=%s", wf.ID, wf.State, wf.PlanVersion, wf.ActiveAgent)))
	if strings.TrimSpace(wf.ActiveTaskID) != "" {
		b.WriteString("\n")
		b.WriteString(styleDim.Render("active task: " + wf.ActiveTaskID))
	}
	if len(wf.CompletedAgents) > 0 {
		var chain []string
		for _, item := range wf.CompletedAgents {
			chain = append(chain, string(item))
		}
		b.WriteString("\n")
		b.WriteString("agent chain: " + strings.Join(chain, " -> "))
	}
	b.WriteString("\n\n")
	b.WriteString(styleHelpHeader.Render("Plan Tasks"))
	for _, task := range wf.Tasks {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("[%s] %s (%s)", task.Status, task.Title, task.OwnerAgent))
		if task.Phase != "" {
			b.WriteString(styleDim.Render("  phase=" + task.Phase))
		}
		if strings.TrimSpace(task.Description) != "" {
			b.WriteString("\n")
			b.WriteString(task.Description)
		}
		if len(task.Dependencies) > 0 {
			b.WriteString("\n")
			b.WriteString(styleDim.Render("depends on: " + strings.Join(task.Dependencies, ", ")))
		}
	}
	if len(wf.RunHistory) > 0 {
		b.WriteString("\n\n")
		b.WriteString(styleHelpHeader.Render("Recent Agent Runs"))
		start := 0
		if len(wf.RunHistory) > 4 {
			start = len(wf.RunHistory) - 4
		}
		for _, run := range wf.RunHistory[start:] {
			b.WriteString("\n")
			b.WriteString(fmt.Sprintf("- %s  task=%s  %s", run.Agent, run.TaskID, run.Reason))
			if strings.TrimSpace(run.Error) != "" {
				b.WriteString("\n")
				b.WriteString(styleDel.Render("  error: " + run.Error))
			} else if strings.TrimSpace(run.Output) != "" {
				b.WriteString("\n")
				b.WriteString(styleDim.Render("  " + oneLine(run.Output)))
			}
		}
	}
	return b.String()
}

func renderPlanChat(wf *domain.CoderWorkflow) string {
	if wf == nil {
		return "(no coder workflow yet)"
	}
	return renderPlanPane(wf)
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
	if frameWidth < 20 {
		frameWidth = 20
	}
	return styleAppFrame.Width(frameWidth).Render(content)
}

func wrapPlainBlock(s string, width int) string {
	if width < 1 {
		width = 1
	}
	return lipgloss.NewStyle().Width(width).Render(s)
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
	w := termWidth
	if w < 8 {
		return 8
	}
	return w
}
