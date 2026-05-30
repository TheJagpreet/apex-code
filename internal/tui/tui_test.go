package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type stubAgent struct {
	model      string
	cwd        string
	session    string
	lazy       bool
	resumed    string
	newCalls   int
	modelCalls []string
	sessions   []SessionOption
}

func (s *stubAgent) Send(_ context.Context, prompt string) (Reply, error) {
	return Reply{Text: "echo: " + prompt}, nil
}
func (s *stubAgent) Stream(_ context.Context, prompt string, onDelta func(string)) (Reply, error) {
	onDelta("echo: " + prompt)
	return Reply{Text: "echo: " + prompt}, nil
}

func (s *stubAgent) Model() string        { return s.model }
func (s *stubAgent) CWD() string          { return s.cwd }
func (s *stubAgent) SessionLabel() string { return s.session }
func (s *stubAgent) LazyTools() bool      { return s.lazy }
func (s *stubAgent) ResumeSession(_ context.Context, selector string) error {
	s.resumed = selector
	s.session = selector
	return nil
}
func (s *stubAgent) NewSession() error {
	s.newCalls++
	s.session = "new"
	return nil
}
func (s *stubAgent) SetModel(_ context.Context, model string) error {
	s.model = model
	s.modelCalls = append(s.modelCalls, model)
	return nil
}
func (s *stubAgent) ListSessions(_ context.Context, _ int) ([]SessionOption, error) {
	return s.sessions, nil
}

func TestStreamFxSettlesAndShimmers(t *testing.T) {
	var fx streamFx
	fx.append("hello world")
	if !fx.active() {
		t.Fatal("fx should be active after append")
	}
	// Before settling, the tail shimmers: rendered output should differ from raw.
	if got := fx.render(); strings.Contains(got, "hello world") {
		t.Fatalf("freshly streamed text should be scrambled, got %q", got)
	}
	// Advancing enough frames settles everything into the real text.
	for i := 0; i < 50; i++ {
		fx.advance()
	}
	if got := fx.render(); !strings.Contains(got, "hello world") {
		t.Fatalf("settled text should reveal real content, got %q", got)
	}
}

func TestRenderBudgetMeter(t *testing.T) {
	out := renderBudgetMeter(BudgetSnapshot{PromptTokens: 50, PromptLimit: 100, OutputHeadroom: 512}, 10, false)
	if !strings.Contains(out, "50/100") {
		t.Fatalf("meter missing usage: %q", out)
	}
	if !strings.Contains(out, "50%") {
		t.Fatalf("meter missing percentage: %q", out)
	}
	if strings.Contains(out, "headroom=512") {
		t.Fatalf("default meter should hide headroom: %q", out)
	}
	if verbose := renderBudgetMeter(BudgetSnapshot{PromptTokens: 50, PromptLimit: 100, OutputHeadroom: 512}, 10, true); !strings.Contains(verbose, "headroom=512") {
		t.Fatalf("verbose meter missing headroom: %q", verbose)
	}

	if got := renderBudgetMeter(BudgetSnapshot{}, 8, false); got == "" {
		t.Fatal("meter should render with empty snapshot")
	}
}

func TestRenderToolInspector(t *testing.T) {
	if !strings.Contains(renderToolInspector(nil), "no tool calls") {
		t.Fatal("empty inspector should say so")
	}
	out := renderToolInspector([]ToolCallView{{Name: "grep", Args: `{"q":"foo"}`}})
	if !strings.Contains(out, "grep") {
		t.Fatalf("inspector missing tool name: %q", out)
	}
}

func TestRenderDiff(t *testing.T) {
	out := renderDiff(DiffView{File: "main.go", Patch: "-old\n+new"})
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "new") {
		t.Fatalf("diff render wrong: %q", out)
	}
}

func TestModelPinUnpin(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "test-model", cwd: "."}, false)
	var err error
	m, _, err = m.command("/pin a.go b.go")
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if pins := m.Pins(); len(pins) != 2 || pins[0] != "a.go" || pins[1] != "b.go" {
		t.Fatalf("pin failed: %v", pins)
	}
	m, _, err = m.command("/unpin a.go")
	if err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if pins := m.Pins(); len(pins) != 1 || pins[0] != "b.go" {
		t.Fatalf("unpin failed: %v", pins)
	}
}

func TestCommandSuggestions(t *testing.T) {
	got := commandSuggestions("/mo")
	if len(got) == 0 || got[0].Label != "/model" {
		t.Fatalf("command suggestions wrong: %+v", got)
	}
}

func TestFileSuggestions(t *testing.T) {
	got := fileSuggestions("@read", []string{"README.md", "internal/tui/"})
	if len(got) == 0 || got[0].Kind != suggestionFile {
		t.Fatalf("file suggestions wrong: %+v", got)
	}
}

func TestCommandModelResumeNew(t *testing.T) {
	agent := &stubAgent{model: "gemma4:e2b", cwd: ".", session: "new"}
	m := New(context.Background(), agent, false)

	var err error
	m, _, err = m.command("/model llama3.1")
	if err != nil {
		t.Fatalf("model: %v", err)
	}
	if agent.model != "llama3.1" {
		t.Fatalf("model not updated: %q", agent.model)
	}
	m, _, err = m.command("/resume latest")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if agent.resumed != "latest" {
		t.Fatalf("resume selector = %q", agent.resumed)
	}
	m, _, err = m.command("/new")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if agent.newCalls != 1 {
		t.Fatalf("new session calls = %d", agent.newCalls)
	}
}

func TestViewShowsWelcomeAndComposer(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "repo", session: "new", lazy: true}, false)
	out := m.View()
	if !strings.Contains(out, "apex-code") {
		t.Fatalf("view missing logo/banner text: %q", out)
	}
	if !strings.Contains(out, "No conversation yet") {
		t.Fatalf("view missing empty state: %q", out)
	}
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") {
		t.Fatalf("view should render inside one outer frame: %q", out)
	}
	if !strings.Contains(out, "Live Stats") {
		t.Fatalf("view missing live stats strip: %q", out)
	}
	if !strings.Contains(out, "companion cat") {
		t.Fatalf("view missing companion badge: %q", out)
	}
	if strings.Contains(out, "headroom=") {
		t.Fatalf("default view should hide verbose budget details: %q", out)
	}
}

func TestNormalizePromptRefs(t *testing.T) {
	got, refs := normalizePromptRefs("Update @.golangci.yml to have a 5 minute timeout")
	if len(refs) != 1 || refs[0] != ".golangci.yml" {
		t.Fatalf("refs = %v", refs)
	}
	if strings.Contains(got, "@.golangci.yml") {
		t.Fatalf("reference was not sanitized: %q", got)
	}
	if !strings.Contains(got, "never include '@'") {
		t.Fatalf("guidance missing: %q", got)
	}
}

func TestCtrlCCancelsRunInsteadOfQuitting(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: ".", session: "new"}, false)
	called := false
	m.working = true
	m.cancelRun = func() { called = true }
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("cancel should not quit while a run is active")
	}
	got := next.(Model)
	if !called || got.working {
		t.Fatalf("cancel not applied: called=%v working=%v", called, got.working)
	}
}

func TestReplyCancellationRendersFriendlyMessage(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: ".", session: "new"}, false)
	got, _ := m.Update(replyMsg{err: context.Canceled})
	model := got.(Model)
	if len(model.transcript) == 0 || !strings.Contains(strings.ToLower(model.transcript[len(model.transcript)-1].Title), "canceled") {
		t.Fatalf("transcript = %+v", model.transcript)
	}
}

func TestUnknownCommandStillErrors(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	_, _, err := m.command("/missing")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnterAcceptsFirstSlashSuggestion(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.input = "/sess"
	m.updateSuggestions()
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("accepting a suggestion should not submit immediately")
	}
	got := next.(Model)
	if got.input != "/sessions " {
		t.Fatalf("input = %q", got.input)
	}
}

func TestEnterSubmitsExactSlashCommand(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.input = "/sessions"
	m.updateSuggestions()
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("submitting an exact slash command should not quit")
	}
	got := next.(Model)
	if len(got.transcript) == 0 || got.transcript[len(got.transcript)-1].Title != "Sessions" {
		t.Fatalf("transcript = %+v", got.transcript)
	}
}

func TestQuitCommandReturnsQuitCmd(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	got, cmd, err := m.command("/quit")
	if err != nil {
		t.Fatalf("quit: %v", err)
	}
	if cmd == nil {
		t.Fatal("quit should return a quit command")
	}
	if !got.quitting {
		t.Fatal("quit should mark the model as quitting")
	}
}

func TestSessionsCommandShowsSessions(t *testing.T) {
	agent := &stubAgent{
		model: "gemma4:e2b",
		cwd:   ".",
		sessions: []SessionOption{
			{ID: "s1", Model: "gemma4:e2b", Title: "first"},
			{ID: "s2", Model: "llama3.1", Title: "second"},
		},
	}
	m := New(context.Background(), agent, false)
	got, _, err := m.command("/sessions")
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(got.transcript) == 0 || !strings.Contains(got.transcript[len(got.transcript)-1].Body, "s1") {
		t.Fatalf("transcript = %+v", got.transcript)
	}
}

func TestClearCommandClearsTranscript(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.transcript = []transcriptEntry{{Title: "old"}}
	m.pane = PaneHelp
	got, _, err := m.command("/clear")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(got.transcript) != 0 {
		t.Fatalf("transcript = %+v", got.transcript)
	}
	if got.pane != PaneChat {
		t.Fatalf("pane = %v", got.pane)
	}
}

func TestPromptsCommandShowsPromptList(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	got, _, err := m.command("/prompts")
	if err != nil {
		t.Fatalf("prompts: %v", err)
	}
	if len(got.transcript) == 0 || !strings.Contains(got.transcript[len(got.transcript)-1].Body, "review") {
		t.Fatalf("transcript = %+v", got.transcript)
	}
	if got.pane != PaneChat {
		t.Fatalf("pane = %v", got.pane)
	}
}

func TestHelpCommandShowsTranscriptEntryAndStaysInChat(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	got, _, err := m.command("/help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if got.pane != PaneChat {
		t.Fatalf("pane = %v", got.pane)
	}
	if len(got.transcript) == 0 || got.transcript[len(got.transcript)-1].Title != "Help" {
		t.Fatalf("transcript = %+v", got.transcript)
	}
	if !strings.Contains(got.transcript[len(got.transcript)-1].Body, "/review") {
		t.Fatalf("help body missing commands: %q", got.transcript[len(got.transcript)-1].Body)
	}
}

func TestDirectPromptStarterCommand(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	got, _, err := m.command("/review")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !strings.Contains(got.input, "Review the recent changes") {
		t.Fatalf("input = %q", got.input)
	}
}

func TestVerboseCommandTogglesDetailMode(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	got, _, err := m.command("/verbose")
	if err != nil {
		t.Fatalf("verbose: %v", err)
	}
	if !got.verbose {
		t.Fatal("verbose mode should be enabled")
	}
	if len(got.transcript) == 0 || !strings.Contains(strings.ToLower(got.transcript[len(got.transcript)-1].Body), "verbose") {
		t.Fatalf("transcript = %+v", got.transcript)
	}
}

func TestAssistantMetaHiddenUntilVerbose(t *testing.T) {
	entry := transcriptEntry{
		Kind:  entryAssistant,
		Title: "apex",
		Body:  "done",
		Meta:  "turns=2  termination=final_answer",
	}
	minimal := renderEntry(entry, false, false)
	if strings.Contains(minimal, "termination=") {
		t.Fatalf("minimal entry should hide meta: %q", minimal)
	}
	verbose := renderEntry(entry, false, true)
	if !strings.Contains(verbose, "termination=final_answer") {
		t.Fatalf("verbose entry should show meta: %q", verbose)
	}
}

func TestRenderHelpPaneShowsTableLikeHeaders(t *testing.T) {
	out := renderHelpPane()
	if !strings.Contains(out, "Command") || !strings.Contains(out, "What it does") {
		t.Fatalf("help headers missing: %q", out)
	}
	if !strings.Contains(out, "Prompt Starters") || !strings.Contains(out, "Purpose") {
		t.Fatalf("prompt starter section missing: %q", out)
	}
	if !strings.Contains(out, "/review") {
		t.Fatalf("direct prompt starter alias missing: %q", out)
	}
}

func TestLoaderRendersAnimatedPhrase(t *testing.T) {
	out := renderLoader(1, 2)
	if !strings.Contains(out, "[/]") {
		t.Fatalf("loader glyph missing: %q", out)
	}
	if !strings.Contains(out, "lining up tools and patches") {
		t.Fatalf("loader render unexpected: %q", out)
	}
}

func TestResumeWithoutArgListsSessions(t *testing.T) {
	agent := &stubAgent{
		model: "gemma4:e2b",
		cwd:   ".",
		sessions: []SessionOption{
			{ID: "s1", Model: "gemma4:e2b", Title: "first"},
		},
	}
	m := New(context.Background(), agent, false)
	got, _, err := m.command("/resume")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(got.transcript) == 0 || !strings.Contains(got.transcript[len(got.transcript)-1].Body, "/resume <id>") {
		t.Fatalf("transcript = %+v", got.transcript)
	}
	if got.input != "/resume " {
		t.Fatalf("input = %q", got.input)
	}
}

func TestTranscriptScrollMarkers(t *testing.T) {
	entries := []transcriptEntry{
		{Title: "1", Body: "a"},
		{Title: "2", Body: "b"},
		{Title: "3", Body: "c"},
		{Title: "4", Body: "d"},
	}
	out := renderTranscript(entries, 2, 1, false)
	if !strings.Contains(out, "older messages above") {
		t.Fatalf("scroll markers missing older hint: %q", out)
	}
	if !strings.Contains(out, "newer messages below") {
		t.Fatalf("scroll markers missing newer hint: %q", out)
	}
}

func TestRenderMarkdownFormatsCommonElements(t *testing.T) {
	out := renderMarkdown("# Title\n- item\n**bold** and `code`")
	if strings.Contains(out, "# Title") {
		t.Fatalf("heading marker should be stripped: %q", out)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "item") {
		t.Fatalf("heading/list text missing: %q", out)
	}
	if strings.Contains(out, "**bold**") {
		t.Fatalf("bold markers should be consumed: %q", out)
	}
	if !strings.Contains(out, "•") {
		t.Fatalf("bullet glyph missing: %q", out)
	}
}

func TestAssistantBodyIsMarkdownRendered(t *testing.T) {
	out := renderEntry(transcriptEntry{Kind: entryAssistant, Title: "apex", Body: "## Heading"}, false, false)
	if strings.Contains(out, "## Heading") {
		t.Fatalf("assistant body should be markdown-rendered: %q", out)
	}
	if !strings.Contains(out, "Heading") {
		t.Fatalf("assistant heading text missing: %q", out)
	}
}

func TestRenderScrollbarThumbMoves(t *testing.T) {
	bottom := renderScrollbar(20, 4, 0, 8)
	top := renderScrollbar(20, 4, 16, 8)
	if !strings.Contains(bottom, "█") || !strings.Contains(top, "█") {
		t.Fatalf("scrollbar thumb missing: bottom=%q top=%q", bottom, top)
	}
	if bottom == top {
		t.Fatal("scrollbar thumb should move as scroll offset changes")
	}
}

func TestMouseWheelScrollsTranscript(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.height = 18
	for i := 0; i < 6; i++ {
		m.transcript = append(m.transcript, transcriptEntry{Title: "t", Body: "b"})
	}
	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	got := next.(Model)
	if got.scrollOffset == 0 {
		t.Fatalf("wheel up should scroll toward older messages, got %d", got.scrollOffset)
	}
	next, _ = got.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if next.(Model).scrollOffset != 0 {
		t.Fatalf("wheel down should return to bottom, got %d", next.(Model).scrollOffset)
	}
}

func TestPetSleepsWhileWorkingAndWandersIdle(t *testing.T) {
	var p petState
	p = p.update(80, true)
	if p.mood != petWaking && p.mood != petSleeping {
		t.Fatalf("working pet should be napping/dozing, mood=%d", p.mood)
	}
	if working := p.render(80, true); !strings.Contains(working, "😴") {
		t.Fatalf("working pet should show sleep emote: %q", working)
	}

	idle := (petState{}).update(80, false)
	out := idle.render(80, false)
	if !strings.Contains(out, "🐱") && !strings.Contains(out, "😺") {
		t.Fatalf("idle pet face missing: %q", out)
	}
}

func TestPetCanCyclePersonas(t *testing.T) {
	p := petState{}
	first := p.currentPersona().Name
	p = p.cyclePersona()
	if p.currentPersona().Name == first {
		t.Fatalf("persona did not change: %q", first)
	}
}

func TestPetSpringMovesTowardTarget(t *testing.T) {
	p := (petState{}).update(80, false)
	p.mood = petWalking
	p.moodTicks = 100
	p.target = float64(p.maxX(80)) // far right
	start := p.pos
	for i := 0; i < 25; i++ {
		p = p.update(80, false)
	}
	if p.pos <= start {
		t.Fatalf("pet should spring toward target: start=%.1f end=%.1f", start, p.pos)
	}
}

func TestViewFitsTerminalHeight(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.width, m.height = 100, 30
	for i := 0; i < 40; i++ {
		m.transcript = append(m.transcript, transcriptEntry{Kind: entryAssistant, Title: "apex", Body: "line of chat"})
	}
	if h := lipgloss.Height(m.View()); h > m.height {
		t.Fatalf("view height %d exceeds terminal height %d", h, m.height)
	}
}

func TestScrollRevealsDifferentContent(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.width, m.height = 100, 30
	for i := 0; i < 40; i++ {
		m.transcript = append(m.transcript, transcriptEntry{Kind: entryAssistant, Title: "apex", Body: fmt.Sprintf("entry-%d", i)})
	}
	bottom := m.View()
	m.scrollToTop()
	if m.scrollOffset == 0 {
		t.Fatal("scrollToTop should move offset above the bottom for an overflowing transcript")
	}
	if top := m.View(); top == bottom {
		t.Fatal("scrolling to the top should reveal different content")
	}
}

func TestUpDownSelectTranscriptWhenComposerEmpty(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.height = 18
	m.transcript = []transcriptEntry{
		{Title: "1", Body: "a"},
		{Title: "2", Body: "b"},
		{Title: "3", Body: "c"},
		{Title: "4", Body: "d"},
		{Title: "5", Body: "e"},
	}
	m.selectedEntry = len(m.transcript) - 1
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	got := next.(Model)
	if got.selectedEntry != len(m.transcript)-2 {
		t.Fatalf("expected selected entry to move up, got %d", got.selectedEntry)
	}
	next, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	got = next.(Model)
	if got.selectedEntry != len(m.transcript)-1 {
		t.Fatalf("expected selected entry to return to the latest message, got %d", got.selectedEntry)
	}
}

func TestCtrlYCopiesSelectedTranscriptEntry(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	m.transcript = []transcriptEntry{
		{Kind: entryUser, Title: "you", Body: "Please fix the bug"},
		{Kind: entryAssistant, Title: "apex", Body: "I updated the code."},
	}
	m.selectedEntry = 1
	var copied string
	prev := clipboardCopy
	clipboardCopy = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardCopy = prev }()

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	got := next.(Model)
	if copied != "apex\nI updated the code." {
		t.Fatalf("copied = %q", copied)
	}
	if !strings.Contains(got.copyStatus, "Copied") {
		t.Fatalf("copy status = %q", got.copyStatus)
	}
}

func TestSelectedEntryShowsCopyHint(t *testing.T) {
	out := renderEntry(transcriptEntry{Kind: entryAssistant, Title: "apex", Body: "done"}, true, false)
	if !strings.Contains(out, "ctrl+y copy") {
		t.Fatalf("selected entry missing copy hint: %q", out)
	}
}

func TestCompanionCommandCyclesPersona(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	first := m.pet.currentPersona().Name
	got, _, err := m.command("/companion")
	if err != nil {
		t.Fatalf("companion: %v", err)
	}
	if got.pet.currentPersona().Name == first {
		t.Fatalf("expected companion to change from %q", first)
	}
	if len(got.transcript) != 0 {
		t.Fatalf("companion switch should not add transcript noise: %+v", got.transcript)
	}
}

func TestF3CyclesTheme(t *testing.T) {
	defer applyTheme(0)
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	first := m.themeIndex
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyF3})
	got := next.(Model)
	if got.themeIndex == first {
		t.Fatalf("expected F3 to change theme from %d", first)
	}
	if len(got.transcript) != 0 {
		t.Fatalf("theme switch should not add transcript noise: %+v", got.transcript)
	}
}

func TestThemeCommandSetsByName(t *testing.T) {
	defer applyTheme(0)
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	got, _, err := m.command("/theme ocean")
	if err != nil {
		t.Fatalf("theme: %v", err)
	}
	if themeName(got.themeIndex) != "ocean" {
		t.Fatalf("expected ocean theme, got %q", themeName(got.themeIndex))
	}
	if _, _, err := m.command("/theme nope"); err == nil {
		t.Fatal("unknown theme should error")
	}
}

func TestViewShowsThemeBadge(t *testing.T) {
	defer applyTheme(0)
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "repo", session: "new"}, false)
	if !strings.Contains(m.View(), "theme emerald") {
		t.Fatalf("view missing theme badge")
	}
}

func TestF2CyclesCompanion(t *testing.T) {
	m := New(context.Background(), &stubAgent{model: "gemma4:e2b", cwd: "."}, false)
	first := m.pet.currentPersona().Name
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyF2})
	got := next.(Model)
	if got.pet.currentPersona().Name == first {
		t.Fatalf("expected F2 to change companion from %q", first)
	}
	if len(got.transcript) != 0 {
		t.Fatalf("F2 companion switch should not add transcript noise: %+v", got.transcript)
	}
}
