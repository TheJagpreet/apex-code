package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ignore "github.com/sabhiram/go-gitignore"
)

// Pane identifies which auxiliary pane is focused for inspection.
type Pane int

const (
	PaneChat Pane = iota
	PaneTools
	PaneDiffs
	PaneContext
	PaneStats
	PaneHelp
	PanePlan
)

func (p Pane) String() string {
	switch p {
	case PaneTools:
		return "tools"
	case PaneDiffs:
		return "diffs"
	case PaneContext:
		return "context"
	case PaneStats:
		return "stats"
	case PaneHelp:
		return "help"
	case PanePlan:
		return "plan"
	default:
		return "chat"
	}
}

type runtimeStatus struct {
	Model     string
	CWD       string
	Session   string
	Companion string
	Theme     string
	ContextSummary string
	LazyTools bool
	Mode      string
}

// replyMsg carries an agent reply back into the Bubble Tea update loop.
type replyMsg struct {
	reply Reply
	err   error
}

type tickMsg time.Time

// streamDeltaMsg carries one chunk of streamed assistant text into the update
// loop. The terminating replyMsg (success or error) arrives on the same channel.
type streamDeltaMsg struct{ text string }

type progressMsg struct {
	reply Reply
}

// waitStream blocks on the streaming channel and surfaces the next event as a
// tea.Msg. It is re-armed after every delta until the final replyMsg arrives.
func waitStream(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// Model is the Bubble Tea model backing the interactive REPL.
type Model struct {
	ctx   context.Context
	agent Agent

	input           string
	history         []string
	historyIndex    int
	transcript      []transcriptEntry
	selectedEntry   int
	working         bool
	pane            Pane
	budget          BudgetSnapshot
	tools           []ToolCallView
	diffs           []DiffView
	stats           string
	pinned          map[string]bool
	recentRefs      []string
	fileIndex       []string
	suggestions     []suggestion
	suggestionIndex int
	scrollOffset    int
	width           int
	height          int
	lastErr         error
	cancelRun       context.CancelFunc
	loaderFrame     int
	loaderPhrase    int
	loaderTicks     int
	pet             petState
	themeIndex      int
	streamFx        streamFx
	streamCh        chan tea.Msg
	copyStatus      string
	verbose         bool
	quitting        bool
	workflow        *domain.CoderWorkflow
}

// New builds a TUI model.
func New(ctx context.Context, agent Agent, verbose bool) Model {
	return Model{
		ctx:           ctx,
		agent:         agent,
		pinned:        map[string]bool{},
		width:         100,
		height:        32,
		historyIndex:  -1,
		selectedEntry: -1,
		fileIndex:     indexProjectEntries(agent.CWD()),
		verbose:       verbose,
		workflow:      agent.CoderWorkflow(),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return tea.Batch(tickCmd(), animCmd()) }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case streamDeltaMsg:
		m.streamFx.append(msg.text)
		m.scrollToBottom()
		return m, waitStream(m.streamCh)

	case replyMsg:
		m.cancelRun = nil
		m.working = false
		m.streamFx = streamFx{}
		m.streamCh = nil
		if msg.err != nil {
			m.lastErr = msg.err
			title := "Run failed"
			body := msg.err.Error()
			if errors.Is(msg.err, context.Canceled) {
				title = "Run canceled"
				body = "Stopped the current agent action."
			}
			m.transcript = append(m.transcript, transcriptEntry{
				Kind:  entryError,
				Title: title,
				Body:  body,
			})
			m.selectLatestEntry()
			m.scrollToBottom()
			return m, nil
		}
		m.lastErr = nil
		if len(m.transcript) > 0 && m.transcript[len(m.transcript)-1].Kind == entryStatus && m.transcript[len(m.transcript)-1].Title == "Workflow progress" {
			m.transcript = m.transcript[:len(m.transcript)-1]
		}
		m.budget = msg.reply.Budget
		m.tools = msg.reply.ToolCalls
		m.diffs = msg.reply.Diffs
		m.stats = msg.reply.Stats
		m.workflow = msg.reply.Workflow
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryAssistant,
			Title: "apex",
			Body:  strings.TrimSpace(msg.reply.Text),
			Meta:  fmt.Sprintf("turns=%d  termination=%s", msg.reply.Turns, msg.reply.Termination),
		})
		m.selectLatestEntry()
		m.scrollToBottom()
		return m, nil

	case progressMsg:
		m.lastErr = nil
		m.budget = msg.reply.Budget
		m.stats = msg.reply.Stats
		m.workflow = msg.reply.Workflow
		if text := strings.TrimSpace(msg.reply.Text); text != "" {
			entry := transcriptEntry{
				Kind:  entryStatus,
				Title: "Workflow progress",
				Body:  text,
			}
			if len(m.transcript) > 0 && m.transcript[len(m.transcript)-1].Kind == entryStatus && m.transcript[len(m.transcript)-1].Title == "Workflow progress" {
				m.transcript[len(m.transcript)-1] = entry
			} else {
				m.transcript = append(m.transcript, entry)
			}
			m.selectLatestEntry()
			m.scrollToBottom()
		}
		return m, waitStream(m.streamCh)

	case tickMsg:
		if m.working {
			m.loaderFrame = (m.loaderFrame + 1) % len(loaderGlyphs)
			m.loaderTicks++
			if m.loaderTicks%5 == 0 {
				m.loaderPhrase = (m.loaderPhrase + 1) % len(loaderPhrases)
			}
		}
		return m, tickCmd()

	case animMsg:
		m.pet = m.pet.update(m.width, m.working)
		if m.working && m.streamFx.active() {
			m.streamFx.advance()
		}
		return m, animCmd()

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.scrollBy(3)
			case tea.MouseButtonWheelDown:
				m.scrollBy(-3)
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "shift+enter" {
		m.input += "\n"
		m.updateSuggestions()
		return m, nil
	}
	switch msg.String() {
	case "alt+p":
		m.pet = m.pet.cyclePersona()
		return m, nil
	case "alt+t":
		m.cycleTheme()
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.working && m.cancelRun != nil {
			m.cancelRun()
			m.cancelRun = nil
			m.working = false
			m.transcript = append(m.transcript, transcriptEntry{
				Kind:  entryStatus,
				Title: "Cancel requested",
				Body:  "Interrupting the current agent turn.",
			})
			m.selectLatestEntry()
			m.scrollToBottom()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyCtrlU:
		m.input = ""
		m.clearSuggestions()
		return m, nil
	case tea.KeyCtrlO:
		m.pane = (m.pane + 1) % 7
		return m, nil
	case tea.KeyCtrlY:
		if err := m.copySelectedEntry(); err != nil {
			m.copyStatus = "Copy failed: " + err.Error()
		} else if m.hasSelectedEntry() {
			m.copyStatus = "Copied selected message to clipboard."
		}
		return m, nil
	case tea.KeyTab:
		if len(m.suggestions) > 0 {
			m.acceptSuggestion()
		} else {
			m.pane = (m.pane + 1) % 7
		}
		return m, nil
	case tea.KeyShiftTab:
		if m.pane == 0 {
			m.pane = 6
		} else {
			m.pane--
		}
		return m, nil
	case tea.KeyEnter:
		if m.shouldAcceptSuggestionOnEnter() {
			m.acceptSuggestion()
			return m, nil
		}
		return m.submit()
	case tea.KeyBackspace:
		if n := len(m.input); n > 0 {
			m.input = m.input[:n-1]
		}
		m.updateSuggestions()
		return m, nil
	case tea.KeyUp:
		if len(m.suggestions) > 0 {
			if m.suggestionIndex > 0 {
				m.suggestionIndex--
			}
			return m, nil
		}
		if strings.TrimSpace(m.input) == "" {
			m.moveSelection(-1)
			return m, nil
		}
		m.recallHistory(-1)
		return m, nil
	case tea.KeyDown:
		if len(m.suggestions) > 0 {
			if m.suggestionIndex < len(m.suggestions)-1 {
				m.suggestionIndex++
			}
			return m, nil
		}
		if strings.TrimSpace(m.input) == "" {
			m.moveSelection(1)
			return m, nil
		}
		m.recallHistory(1)
		return m, nil
	case tea.KeyPgUp:
		m.scrollBy(m.pageStep())
		return m, nil
	case tea.KeyPgDown:
		m.scrollBy(-m.pageStep())
		return m, nil
	case tea.KeyHome:
		m.scrollToTop()
		return m, nil
	case tea.KeyEnd:
		m.scrollToBottom()
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		m.input += string(msg.Runes)
		m.updateSuggestions()
		return m, nil
	}
	return m, nil
}

func (m *Model) recallHistory(delta int) {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex < 0 {
		m.historyIndex = len(m.history)
	}
	m.historyIndex += delta
	if m.historyIndex < 0 {
		m.historyIndex = 0
	}
	if m.historyIndex >= len(m.history) {
		m.historyIndex = len(m.history)
		m.input = ""
		return
	}
	m.input = m.history[m.historyIndex]
	m.updateSuggestions()
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	prompt := strings.TrimSpace(m.input)
	if prompt == "" || m.working {
		return m, nil
	}
	m.history = append(m.history, prompt)
	m.historyIndex = len(m.history)
	m.input = ""
	m.clearSuggestions()

	if strings.HasPrefix(prompt, "/") {
		next, cmd, err := m.command(prompt)
		if err != nil {
			next.transcript = append(next.transcript, transcriptEntry{
				Kind:  entryError,
				Title: "Command failed",
				Body:  err.Error(),
			})
			next.selectLatestEntry()
		}
		return next, cmd
	}

	rawPrompt := prompt
	prompt, refs := normalizePromptRefs(prompt)
	if len(refs) > 0 {
		next := m
		next.appendRefs(refs)
		m = next
	}
	m.transcript = append(m.transcript, transcriptEntry{
		Kind:  entryUser,
		Title: "you",
		Body:  rawPrompt,
	})
	m.selectLatestEntry()
	m.scrollToBottom()
	m.working = true
	m.streamFx = streamFx{}

	agent := m.agent
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelRun = cancel

	// Buffered so the agent goroutine never blocks on a slow frame, and so it can
	// keep streaming between Bubble Tea update ticks.
	ch := make(chan tea.Msg, 256)
	m.streamCh = ch
	go func() {
		defer cancel()
		defer close(ch)
		var (
			reply Reply
			err   error
		)
		if agent.Mode() == "coder" {
			reply, err = agent.CoderSubmit(ctx, prompt)
		} else {
			reply, err = agent.Stream(ctx, prompt, func(delta string) {
				select {
				case ch <- streamDeltaMsg{text: delta}:
				case <-ctx.Done():
				}
			})
		}
		ch <- replyMsg{reply: reply, err: err}
	}()
	return m, waitStream(ch)
}

func (m Model) startCoderAction(title string, fn func(context.Context) (Reply, error)) (tea.Model, tea.Cmd) {
	if m.working {
		return m, nil
	}
	m.transcript = append(m.transcript, transcriptEntry{
		Kind:  entryStatus,
		Title: title,
		Body:  "Working...",
	})
	m.selectLatestEntry()
	m.scrollToBottom()
	m.working = true
	m.streamFx = streamFx{}

	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelRun = cancel
	ch := make(chan tea.Msg, 8)
	m.streamCh = ch
	go func() {
		defer cancel()
		defer close(ch)
		reply, err := fn(ctx)
		ch <- replyMsg{reply: reply, err: err}
	}()
	return m, waitStream(ch)
}

func (m Model) startCoderStreamAction(title string, fn func(context.Context, chan tea.Msg) (Reply, error)) (tea.Model, tea.Cmd) {
	if m.working {
		return m, nil
	}
	m.transcript = append(m.transcript, transcriptEntry{
		Kind:  entryStatus,
		Title: title,
		Body:  "Working...",
	})
	m.selectLatestEntry()
	m.scrollToBottom()
	m.working = true
	m.streamFx = streamFx{}

	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelRun = cancel
	ch := make(chan tea.Msg, 32)
	m.streamCh = ch
	go func() {
		defer cancel()
		defer close(ch)
		reply, err := fn(ctx, ch)
		ch <- replyMsg{reply: reply, err: err}
	}()
	return m, waitStream(ch)
}

func (m Model) command(cmd string) (Model, tea.Cmd, error) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return m, nil, nil
	}
	switch fields[0] {
	case "/help":
		m.pane = PaneChat
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryStatus,
			Title: "Help",
			Body:  renderHelpPane(),
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/pin":
		for _, f := range fields[1:] {
			m.pinned[f] = true
		}
	case "/unpin":
		for _, f := range fields[1:] {
			delete(m.pinned, f)
		}
	case "/pane":
		if len(fields) > 1 {
			m.pane = paneFromName(fields[1])
		}
	case "/plan":
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryAssistant,
			Title: "apex",
			Body:  renderPlanChat(m.workflow),
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/mode":
		if len(fields) == 1 {
			m.transcript = append(m.transcript, transcriptEntry{
				Kind:  entryStatus,
				Title: "Mode",
				Body:  "Current mode: " + m.agent.Mode(),
			})
			m.selectLatestEntry()
			m.scrollToBottom()
			break
		}
		if err := m.agent.SetMode(m.ctx, fields[1]); err != nil {
			return m, nil, err
		}
		m.workflow = m.agent.CoderWorkflow()
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryStatus,
			Title: "Mode switched",
			Body:  "Active mode is now " + m.agent.Mode(),
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/approve":
		next, cmd := m.startCoderStreamAction("Approving plan", func(ctx context.Context, ch chan tea.Msg) (Reply, error) {
			if _, err := m.agent.CoderApprove(ctx); err != nil {
				return Reply{}, err
			}
			return m.agent.CoderExecuteStream(ctx, func(reply Reply) {
				select {
				case ch <- progressMsg{reply: reply}:
				case <-ctx.Done():
				}
			})
		})
		return next.(Model), cmd, nil
	case "/replan":
		feedback := strings.TrimSpace(strings.TrimPrefix(cmd, fields[0]))
		if feedback == "" {
			return m, nil, fmt.Errorf("usage: /replan <feedback>")
		}
		next, cmd := m.startCoderAction("Replanning", func(ctx context.Context) (Reply, error) {
			return m.agent.CoderReview(ctx, feedback)
		})
		return next.(Model), cmd, nil
	case "/runplan":
		next, cmd := m.startCoderStreamAction("Running plan", func(ctx context.Context, ch chan tea.Msg) (Reply, error) {
			return m.agent.CoderExecuteStream(ctx, func(reply Reply) {
				select {
				case ch <- progressMsg{reply: reply}:
				case <-ctx.Done():
				}
			})
		})
		return next.(Model), cmd, nil
	case "/quit":
		m.quitting = true
		return m, tea.Quit, nil
	case "/clear":
		m.transcript = nil
		m.selectedEntry = -1
		m.tools = nil
		m.diffs = nil
		m.recentRefs = nil
		m.input = ""
		m.clearSuggestions()
		m.lastErr = nil
		m.pane = PaneChat
		m.scrollToBottom()
	case "/stats":
		m.pane = PaneStats
	case "/prompts":
		var names []string
		for _, prompt := range starterPrompts {
			names = append(names, fmt.Sprintf("/%s  %s", prompt.Name, prompt.Description))
		}
		m.pane = PaneChat
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryStatus,
			Title: "Prompt starters",
			Body:  strings.Join(names, "\n"),
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/companion":
		m.pet = m.pet.cyclePersona()
	case "/theme":
		if len(fields) > 1 {
			if !m.setThemeByName(fields[1]) {
				return m, nil, fmt.Errorf("unknown theme %q (try one of: %s)", fields[1], themeNames())
			}
		} else {
			m.cycleTheme()
		}
	case "/explain", "/review", "/fix", "/test":
		prompt, ok := lookupPrompt(strings.TrimPrefix(fields[0], "/"))
		if !ok {
			return m, nil, fmt.Errorf("unknown prompt starter %q", fields[0])
		}
		m.input = prompt.Body
		m.updateSuggestions()
	case "/verbose":
		m.verbose = !m.verbose
		mode := "minimal"
		if m.verbose {
			mode = "verbose"
		}
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryStatus,
			Title: "View mode",
			Body:  "Switched to " + mode + " detail mode.",
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/model":
		if len(fields) == 1 {
			m.transcript = append(m.transcript, transcriptEntry{
				Kind:  entryStatus,
				Title: "Model",
				Body:  "Current model: " + m.agent.Model(),
			})
			m.selectLatestEntry()
			m.scrollToBottom()
			break
		}
		if err := m.agent.SetModel(m.ctx, fields[1]); err != nil {
			return m, nil, err
		}
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryStatus,
			Title: "Model switched",
			Body:  "Active model is now " + m.agent.Model(),
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/sessions":
		sessions, err := m.agent.ListSessions(m.ctx, 8)
		if err != nil {
			return m, nil, err
		}
		m.pane = PaneContext
		if len(sessions) == 0 {
			m.transcript = append(m.transcript, transcriptEntry{Kind: entryStatus, Title: "Sessions", Body: "No saved sessions yet."})
			m.selectLatestEntry()
			m.scrollToBottom()
			break
		}
		lines := make([]string, 0, len(sessions))
		for _, session := range sessions {
			lines = append(lines, fmt.Sprintf("%s  %s  %s", session.ID, session.Model, session.Title))
		}
		m.transcript = append(m.transcript, transcriptEntry{Kind: entryStatus, Title: "Recent sessions", Body: strings.Join(lines, "\n")})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/resume":
		if len(fields) == 1 {
			sessions, err := m.agent.ListSessions(m.ctx, 8)
			if err != nil {
				return m, nil, err
			}
			m.pane = PaneContext
			if len(sessions) == 0 {
				m.transcript = append(m.transcript, transcriptEntry{
					Kind:  entryStatus,
					Title: "Resume session",
					Body:  "No saved sessions yet.",
				})
				m.selectLatestEntry()
				m.scrollToBottom()
				break
			}
			lines := []string{"Select a session by running `/resume <id>`:"}
			for _, session := range sessions {
				lines = append(lines, fmt.Sprintf("%s  %s  %s", session.ID, session.Model, session.Title))
			}
			m.input = "/resume "
			m.transcript = append(m.transcript, transcriptEntry{
				Kind:  entryStatus,
				Title: "Resume session",
				Body:  strings.Join(lines, "\n"),
			})
			m.selectLatestEntry()
			m.scrollToBottom()
			break
		}
		selector := fields[1]
		if err := m.agent.ResumeSession(m.ctx, selector); err != nil {
			return m, nil, err
		}
		m.transcript = nil
		m.recentRefs = nil
		m.tools = nil
		m.diffs = nil
		m.lastErr = nil
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryStatus,
			Title: "Session resumed",
			Body:  "Loaded " + m.agent.SessionLabel(),
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	case "/new":
		if err := m.agent.NewSession(); err != nil {
			return m, nil, err
		}
		m.transcript = nil
		m.recentRefs = nil
		m.tools = nil
		m.diffs = nil
		m.stats = ""
		m.lastErr = nil
		m.transcript = append(m.transcript, transcriptEntry{
			Kind:  entryStatus,
			Title: "New session",
			Body:  "Started a clean conversation window.",
		})
		m.selectLatestEntry()
		m.scrollToBottom()
	default:
		return m, nil, fmt.Errorf("unknown command: %s", fields[0])
	}
	return m, nil, nil
}

// cycleTheme advances to the next color theme and applies it immediately.
func (m *Model) cycleTheme() {
	if len(themes) == 0 {
		return
	}
	m.themeIndex = (m.themeIndex + 1) % len(themes)
	applyTheme(m.themeIndex)
}

// setThemeByName selects a theme by its name, returning false if none matches.
func (m *Model) setThemeByName(name string) bool {
	for i, t := range themes {
		if strings.EqualFold(t.Name, name) {
			m.themeIndex = i
			applyTheme(i)
			return true
		}
	}
	return false
}

// themeNames lists the available theme names for help and error messages.
func themeNames() string {
	names := make([]string, 0, len(themes))
	for _, t := range themes {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// Pins returns the sorted list of manually pinned context items.
func (m Model) Pins() []string {
	out := make([]string, 0, len(m.pinned))
	for p := range m.pinned {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return "bye\n"
	}
	status := runtimeStatus{
		Model:     m.agent.Model(),
		CWD:       filepath.Base(m.agent.CWD()),
		Session:   m.agent.SessionLabel(),
		Companion: m.pet.currentPersona().Name,
		Theme:     themeName(m.themeIndex),
		ContextSummary: renderBudgetCompact(m.budget),
		LazyTools: m.agent.LazyTools(),
		Mode:      m.agent.Mode(),
	}
	inner := m.frameInnerWidth()
	header := wrapTo(m.headerView(status), inner)
	chrome := wrapTo(m.renderChrome(status), inner)
	conv := m.renderConversation(m.conversationHeight(header, chrome), inner)

	body := header + "\n\n" + conv + "\n\n" + chrome
	return renderAppFrame(body, m.width)
}

func (m Model) hasSelectedEntry() bool {
	return m.selectedEntry >= 0 && m.selectedEntry < len(m.transcript)
}

func (m *Model) selectLatestEntry() {
	if len(m.transcript) == 0 {
		m.selectedEntry = -1
		return
	}
	m.selectedEntry = len(m.transcript) - 1
}

func (m *Model) moveSelection(delta int) {
	if len(m.transcript) == 0 {
		return
	}
	if !m.hasSelectedEntry() {
		m.selectLatestEntry()
		return
	}
	m.selectedEntry += delta
	if m.selectedEntry < 0 {
		m.selectedEntry = 0
	}
	if m.selectedEntry >= len(m.transcript) {
		m.selectedEntry = len(m.transcript) - 1
	}
	if delta < 0 {
		m.scrollBy(1)
	} else if delta > 0 {
		m.scrollBy(-1)
	}
}

func (m *Model) copySelectedEntry() error {
	if !m.hasSelectedEntry() {
		return nil
	}
	return clipboardCopy(m.transcript[m.selectedEntry].rawText())
}

// headerView is the full landing banner before any conversation, and a compact
// two-line header once chat has started so the transcript gets more room.
func (m Model) headerView(status runtimeStatus) string {
	if len(m.transcript) == 0 {
		return renderWelcome(status)
	}
	return renderHeaderCompact(status)
}

// frameInnerWidth is the usable text width inside the app frame, accounting for
// the rounded border and horizontal padding applied by renderAppFrame.
func (m Model) frameInnerWidth() int {
	return innerWidthFromFrame(m.width)
}

func innerWidthFromFrame(width int) int {
	frameWidth := width - 2
	if frameWidth < 20 {
		frameWidth = 20
	}
	inner := frameWidth - 6 // border (2) + horizontal padding (2*2)
	if inner < 8 {
		inner = 8
	}
	return inner
}

// wrapTo hard-wraps s to width so that downstream line counting and the app
// frame agree on height (the frame re-wraps anything wider than the box).
func wrapTo(s string, width int) string {
	if width < 1 {
		width = 1
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

// renderChrome builds everything shown below the conversation: the optional
// loader, the focused pane, the live stats strip, the composer, the status
// footer, and the footer pet. Its height is fixed per frame so the
// conversation viewport can claim the remaining rows.
func (m Model) renderChrome(status runtimeStatus) string {
	var b strings.Builder
	hasSection := false
	if m.working {
		b.WriteString(renderLoader(m.loaderFrame, m.loaderPhrase))
		hasSection = true
	}
	switch m.pane {
	case PaneTools:
		if hasSection {
			b.WriteString("\n\n")
		}
		b.WriteString(renderToolInspector(m.tools))
		hasSection = true
	case PaneDiffs:
		if hasSection {
			b.WriteString("\n\n")
		}
		b.WriteString(renderDiffs(m.diffs))
		hasSection = true
	case PaneContext:
		if hasSection {
			b.WriteString("\n\n")
		}
		b.WriteString(renderContextPane(m.Pins(), m.recentRefs))
		hasSection = true
	case PaneStats:
		if hasSection {
			b.WriteString("\n\n")
		}
		b.WriteString(renderStatsPane(m.budget, m.stats, m.verbose))
		hasSection = true
	case PaneHelp:
		if hasSection {
			b.WriteString("\n\n")
		}
		b.WriteString(renderHelpPane())
		hasSection = true
	case PanePlan:
		if hasSection {
			b.WriteString("\n\n")
		}
		b.WriteString(renderPlanPane(m.workflow))
		hasSection = true
	default:
		// Keep the chat area visually quiet in the default pane.
	}
	if hasSection {
		b.WriteString("\n\n")
	}
	b.WriteString(renderComposer(m.input, m.suggestions, m.suggestionIndex, m.pane, innerWidthFromFrame(m.width), m.verbose, m.copyStatus, m.working))
	if footer := renderStatusFooter(status, m.pane); strings.TrimSpace(footer) != "" {
		b.WriteString("\n")
		b.WriteString(footer)
	}
	b.WriteString("\n")
	b.WriteString(m.pet.render(m.width, m.working))
	return b.String()
}

// renderConversation renders the transcript into at most height lines, honoring
// the current line-based scroll offset and attaching a scrollbar when the
// conversation overflows the viewport.
func (m Model) renderConversation(height, inner int) string {
	if len(m.transcript) == 0 {
		return styleConversationFrame.Width(inner).Render(styleDim.Render("No conversation yet. Try `@README.md summarize this repo` or `/review`."))
	}
	frameInner := inner - 2
	if frameInner < 16 {
		frameInner = 16
	}
	contentHeight := height - 2
	if contentHeight < 1 {
		contentHeight = 1
	}
	// Leave two columns for the scrollbar (" " + bar) so wrapped lines plus the
	// bar never exceed the frame's inner width.
	content := renderAllEntries(m.transcript, m.selectedEntry, m.verbose)
	if m.working && m.streamFx.active() {
		if content != "" {
			content += "\n\n"
		}
		content += renderStreamEntry(m.streamFx)
	}
	wrapped := wrapTo(content, frameInner-2)
	lines := strings.Split(wrapped, "\n")
	total := len(lines)
	if total <= contentHeight {
		return styleConversationFrame.Width(inner).Render(wrapped)
	}
	end := total - m.scrollOffset
	if end > total {
		end = total
	}
	if end < contentHeight {
		end = contentHeight
	}
	start := end - contentHeight
	window := strings.Join(lines[start:end], "\n")
	bar := renderScrollbar(total, contentHeight, m.scrollOffset, contentHeight)
	return styleConversationFrame.Width(inner).Render(lipgloss.JoinHorizontal(lipgloss.Top, window, " ", bar))
}

// Run starts the interactive TUI program.
func Run(ctx context.Context, agent Agent, verbose bool) error {
	p := tea.NewProgram(New(ctx, agent, verbose), tea.WithContext(ctx), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func (m *Model) clearSuggestions() {
	m.suggestions = nil
	m.suggestionIndex = 0
}

func (m *Model) updateSuggestions() {
	if suggestions := modeSuggestions(m.input); len(suggestions) > 0 {
		m.suggestions = suggestions
		if m.suggestionIndex >= len(m.suggestions) {
			m.suggestionIndex = 0
		}
		return
	}
	token := currentToken(m.input)
	switch {
	case strings.HasPrefix(token, "/"):
		m.suggestions = commandSuggestions(token)
	case strings.HasPrefix(token, "@"):
		m.suggestions = fileSuggestions(token, m.fileIndex)
	default:
		m.suggestions = nil
	}
	if m.suggestionIndex >= len(m.suggestions) {
		m.suggestionIndex = 0
	}
}

func (m Model) shouldAcceptSuggestionOnEnter() bool {
	if len(m.suggestions) == 0 {
		return false
	}
	if suggestions := modeSuggestions(m.input); len(suggestions) > 0 {
		selected := m.suggestions[m.suggestionIndex]
		return strings.TrimSpace(m.input) != strings.TrimSpace(selected.Insert) && strings.TrimSpace(m.input) != selected.Label
	}
	token := currentToken(m.input)
	if token == "" {
		return false
	}
	s := m.suggestions[m.suggestionIndex]
	expected := strings.TrimSpace(s.Insert)
	if token == expected || token == s.Label {
		return false
	}
	return strings.HasPrefix(token, "/") || strings.HasPrefix(token, "@")
}

func (m *Model) acceptSuggestion() {
	if len(m.suggestions) == 0 {
		return
	}
	s := m.suggestions[m.suggestionIndex]
	if s.Replace != "" && strings.HasSuffix(m.input, s.Replace) {
		m.input = strings.TrimSuffix(m.input, s.Replace) + s.Insert
	} else if s.Replace != "" && strings.HasSuffix(strings.TrimRight(m.input, " "), s.Replace) {
		trimmed := strings.TrimRight(m.input, " ")
		m.input = strings.TrimSuffix(trimmed, s.Replace) + s.Insert
	} else {
		m.input += s.Insert
	}
	m.clearSuggestions()
}

func (m *Model) appendRefs(refs []string) {
	seen := map[string]bool{}
	for _, ref := range m.recentRefs {
		seen[ref] = true
	}
	for _, ref := range refs {
		if !seen[ref] {
			m.recentRefs = append([]string{ref}, m.recentRefs...)
			seen[ref] = true
		}
	}
	if len(m.recentRefs) > 8 {
		m.recentRefs = m.recentRefs[:8]
	}
}

// conversationHeight returns how many lines the conversation viewport may use,
// given the already-rendered header and chrome blocks. The remainder of the
// terminal height (minus the app frame's border/padding and the two blank
// separator rows) is handed to the transcript.
func (m *Model) conversationHeight(header, chrome string) int {
	height := m.height - lipgloss.Height(header) - lipgloss.Height(chrome) - 6
	if height < 1 {
		height = 1
	}
	return height
}

// scrollMetrics recomputes the conversation viewport height and total rendered
// line count so key handlers can clamp the scroll offset the same way View
// does. It mirrors View's layout exactly.
func (m *Model) scrollMetrics() (total, height int) {
	status := runtimeStatus{
		Model:     m.agent.Model(),
		CWD:       filepath.Base(m.agent.CWD()),
		Session:   m.agent.SessionLabel(),
		Companion: m.pet.currentPersona().Name,
		Theme:     themeName(m.themeIndex),
		ContextSummary: renderBudgetCompact(m.budget),
		LazyTools: m.agent.LazyTools(),
		Mode:      m.agent.Mode(),
	}
	inner := m.frameInnerWidth()
	height = m.conversationHeight(wrapTo(m.headerView(status), inner), wrapTo(m.renderChrome(status), inner))
	if len(m.transcript) == 0 {
		return 0, height
	}
	total = lipgloss.Height(wrapTo(renderAllEntries(m.transcript, m.selectedEntry, m.verbose), inner-2))
	return total, height
}

func (m *Model) maxScrollOffset() int {
	total, height := m.scrollMetrics()
	if maxOffset := total - height; maxOffset > 0 {
		return maxOffset
	}
	return 0
}

func (m *Model) scrollBy(delta int) {
	m.scrollOffset += delta
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
	if maxOffset := m.maxScrollOffset(); m.scrollOffset > maxOffset {
		m.scrollOffset = maxOffset
	}
}

func (m *Model) scrollToBottom() {
	m.scrollOffset = 0
}

func (m *Model) scrollToTop() {
	m.scrollOffset = m.maxScrollOffset()
}

func (m *Model) pageStep() int {
	_, height := m.scrollMetrics()
	if height < 2 {
		return 1
	}
	return height - 1
}

func paneFromName(name string) Pane {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "tools":
		return PaneTools
	case "diffs":
		return PaneDiffs
	case "context":
		return PaneContext
	case "stats":
		return PaneStats
	case "help":
		return PaneHelp
	case "plan":
		return PanePlan
	default:
		return PaneChat
	}
}

func lookupPrompt(name string) (promptSpec, bool) {
	for _, prompt := range starterPrompts {
		if prompt.Name == name {
			return prompt, true
		}
	}
	return promptSpec{}, false
}

func currentToken(input string) string {
	if input == "" {
		return ""
	}
	idx := strings.LastIndexAny(input, " \n\t")
	if idx < 0 {
		return input
	}
	return input[idx+1:]
}

func commandSuggestions(token string) []suggestion {
	token = strings.ToLower(token)
	var out []suggestion
	for _, cmd := range slashCommands {
		if strings.HasPrefix(cmd.Name, token) {
			out = append(out, suggestion{
				Label:   cmd.Name,
				Insert:  cmd.Name + " ",
				Detail:  cmd.Description,
				Kind:    suggestionCommand,
				Replace: token,
			})
		}
	}
	return limitSuggestions(out)
}

func modeSuggestions(input string) []suggestion {
	trimmed := strings.TrimSpace(strings.ToLower(input))
	if trimmed == "" || !strings.HasPrefix(trimmed, "/mode") {
		return nil
	}
	if strings.HasPrefix(trimmed, "/mode chat") || strings.HasPrefix(trimmed, "/mode coder") {
		return nil
	}
	query := strings.TrimSpace(strings.TrimPrefix(trimmed, "/mode"))
	options := []suggestion{
		{Label: "/mode chat", Insert: "/mode chat", Detail: "switch to normal chat mode", Kind: suggestionCommand, Replace: strings.TrimSpace(input)},
		{Label: "/mode coder", Insert: "/mode coder", Detail: "switch to coder workflow mode", Kind: suggestionCommand, Replace: strings.TrimSpace(input)},
	}
	if query == "" {
		return options
	}
	var ranked []suggestion
	for _, option := range options {
		mode := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(option.Label), "/mode"))
		if strings.HasPrefix(mode, query) {
			ranked = append(ranked, option)
		}
	}
	if len(ranked) > 0 {
		return ranked
	}
	for _, option := range options {
		mode := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(option.Label), "/mode"))
		if strings.Contains(mode, query) {
			ranked = append(ranked, option)
		}
	}
	return ranked
}

func fileSuggestions(token string, files []string) []suggestion {
	query := strings.TrimPrefix(strings.ToLower(token), "@")
	var out []suggestion
	for _, file := range files {
		lower := strings.ToLower(file)
		if query == "" || strings.Contains(lower, query) {
			out = append(out, suggestion{
				Label:   "@" + file,
				Insert:  "@" + file + " ",
				Detail:  fileRefDetail(file),
				Kind:    suggestionFile,
				Replace: token,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Label) < len(out[j].Label) })
	return limitSuggestions(out)
}

func limitSuggestions(in []suggestion) []suggestion {
	if len(in) > 8 {
		return in[:8]
	}
	return in
}

func extractRefs(prompt string) []string {
	fields := strings.Fields(prompt)
	var refs []string
	for _, field := range fields {
		if strings.HasPrefix(field, "@") && len(field) > 1 {
			refs = append(refs, strings.Trim(strings.TrimPrefix(field, "@"), ",;:()[]{}\"'"))
		}
	}
	return refs
}

func normalizePromptRefs(prompt string) (string, []string) {
	refs := extractRefs(prompt)
	if len(refs) == 0 {
		return prompt, nil
	}
	sanitized := prompt
	for _, ref := range refs {
		sanitized = strings.ReplaceAll(sanitized, "@"+ref, ref)
	}
	var b strings.Builder
	b.WriteString(sanitized)
	b.WriteString("\n\nReferenced files (use these exact relative paths in tool calls; never include '@'):\n")
	for _, ref := range refs {
		b.WriteString("- ")
		b.WriteString(ref)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), refs
}

func indexProjectEntries(root string) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	var files []string
	skip := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, ".gocache": true, "dist": true, "build": true,
	}
	matcher := loadGitIgnore(root)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() && skip[name] {
			return filepath.SkipDir
		}
		if matcher != nil && matcher.MatchesPath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			files = append(files, rel+"/")
		} else {
			info, statErr := d.Info()
			if statErr == nil && info.Size() > 1<<20 {
				return nil
			}
			files = append(files, rel)
		}
		if len(files) >= 600 {
			return fs.SkipAll
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func loadGitIgnore(root string) *ignore.GitIgnore {
	path := filepath.Join(root, ".gitignore")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	matcher, err := ignore.CompileIgnoreFile(path)
	if err != nil {
		return nil
	}
	return matcher
}

func meterWidth(termWidth int) int {
	w := termWidth - 64
	if w < 10 {
		return 10
	}
	if w > 40 {
		return 40
	}
	return w
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ = os.ErrNotExist

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

type animMsg time.Time

// animCmd drives the footer pet at roughly 8 fps so its spring motion looks
// smooth without busy-spinning the terminal.
func animCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg {
		return animMsg(t)
	})
}

func fileRefDetail(path string) string {
	if strings.HasSuffix(path, "/") {
		return "folder reference"
	}
	return "file reference"
}
