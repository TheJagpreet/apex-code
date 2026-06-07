package tui_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

type stubAgent struct{}

func (stubAgent) Send(context.Context, string) (tui.Reply, error) { return tui.Reply{}, nil }
func (stubAgent) Stream(context.Context, string, func(string)) (tui.Reply, error) {
	return tui.Reply{}, nil
}
func (stubAgent) Model() string                                                  { return "deepseek-v4-flash" }
func (stubAgent) CWD() string                                                    { return "repo" }
func (stubAgent) SessionLabel() string                                           { return "1234567890abcdef" }
func (stubAgent) LazyTools() bool                                                { return false }
func (stubAgent) ResumeSession(context.Context, string) error                    { return nil }
func (stubAgent) NewSession() error                                              { return nil }
func (stubAgent) SetModel(context.Context, string) error                         { return nil }
func (stubAgent) ListSessions(context.Context, int) ([]tui.SessionOption, error) { return nil, nil }
func (stubAgent) Mode() string                                                   { return "chat" }
func (stubAgent) SetMode(context.Context, string) error                          { return nil }
func (stubAgent) CoderSubmit(context.Context, string) (tui.Reply, error)         { return tui.Reply{}, nil }
func (stubAgent) CoderReview(context.Context, string) (tui.Reply, error)         { return tui.Reply{}, nil }
func (stubAgent) CoderApprove(context.Context) (tui.Reply, error)                { return tui.Reply{}, nil }
func (stubAgent) CoderExecute(context.Context) (tui.Reply, error)                { return tui.Reply{}, nil }
func (stubAgent) CoderExecuteStream(context.Context, func(tui.Reply)) (tui.Reply, error) {
	return tui.Reply{}, nil
}
func (stubAgent) CoderWorkflow() *domain.CoderWorkflow          { return nil }
func (stubAgent) LiveStatus(context.Context) (tui.Reply, error) { return tui.Reply{}, nil }
func (stubAgent) Extensions() tui.ExtensionView {
	return tui.ExtensionView{
		AvailableAgents:  []tui.BundleHeaderView{{Name: "frontend", File: "frontend.md"}},
		AvailableSkills:  []tui.BundleHeaderView{{Name: "tailwind", File: "tailwind.md"}, {Name: "accessibility", File: "accessibility.md"}},
		ActiveAgent:      "frontend",
		ActiveAgentFile:  "frontend.md",
		ActiveSkills:     []string{"tailwind", "accessibility"},
		ActiveSkillFiles: []string{"tailwind.md", "accessibility.md"},
	}
}
func (stubAgent) ReloadExtensions(context.Context) (tui.ExtensionView, error) {
	return stubAgent{}.Extensions(), nil
}
func (stubAgent) SetActiveAgent(context.Context, string) error { return nil }
func (stubAgent) ActivateSkill(context.Context, string) error  { return nil }

type slashAgentStub struct {
	stubAgent
	loadedAgent  string
	lastPrompt   string
	loadedSkills []string
}

func (a *slashAgentStub) Extensions() tui.ExtensionView {
	return tui.ExtensionView{
		AvailableAgents: []tui.BundleHeaderView{{Name: "frontend", File: "frontend.md", Aliases: []string{"ui"}, Skills: []string{"testing"}}},
		AvailableSkills: []tui.BundleHeaderView{{Name: "docs", File: "docs.md"}, {Name: "testing", File: "testing.md"}},
	}
}

func (a *slashAgentStub) SetActiveAgent(_ context.Context, name string) error {
	a.loadedAgent = name
	return nil
}

func (a *slashAgentStub) Stream(_ context.Context, prompt string, _ func(string)) (tui.Reply, error) {
	a.lastPrompt = prompt
	return tui.Reply{Text: "done"}, nil
}

func (a *slashAgentStub) ActivateSkill(_ context.Context, name string) error {
	a.loadedSkills = append(a.loadedSkills, name)
	return nil
}

type modeSwitchAgentStub struct {
	stubAgent
	mode        string
	activeAgent string
}

func (a *modeSwitchAgentStub) Extensions() tui.ExtensionView {
	return tui.ExtensionView{
		ActiveAgent:     a.activeAgent,
		ActiveAgentFile: a.activeAgent,
	}
}

func (a *modeSwitchAgentStub) SetMode(_ context.Context, mode string) error {
	a.mode = mode
	return nil
}

func (a *modeSwitchAgentStub) SetActiveAgent(_ context.Context, name string) error {
	a.activeAgent = name
	return nil
}

type coderProgressAgentStub struct {
	stubAgent
	mode string
}

func (a *coderProgressAgentStub) Mode() string { return "coder" }

func (a *coderProgressAgentStub) CoderExecuteStream(_ context.Context, onUpdate func(tui.Reply)) (tui.Reply, error) {
	onUpdate(tui.Reply{
		Text:              "reviewer completed README review\n\n| S.No | Description | Fix |\n| --- | --- | --- |\n| 1 | Missing troubleshooting section | Add quick first-run diagnostics |",
		ProgressKind:      "completed",
		ProgressAgent:     "reviewer",
		ProgressTaskTitle: "Review README.md",
		ProgressSummary:   "| S.No | Description | Fix |\n| --- | --- | --- |\n| 1 | Missing troubleshooting section | Add quick first-run diagnostics |",
	})
	return tui.Reply{Text: "Coder workflow execution completed."}, nil
}

func TestNewViewShowsCoreChrome(t *testing.T) {
	model := tui.New(context.Background(), stubAgent{}, false)
	out := model.View()
	if !strings.Contains(out, "apex-code") {
		t.Fatalf("view missing product name: %q", out)
	}
	if !strings.Contains(out, "No conversation yet") {
		t.Fatalf("view missing empty state: %q", out)
	}
	if strings.Contains(out, "session 1234567890abcdef") {
		t.Fatalf("view should show compact session id without session prefix: %q", out)
	}
	if !strings.Contains(out, "frontend.md") || !strings.Contains(out, "tailwind.md+1") || !strings.Contains(out, "agents 1") || !strings.Contains(out, "skills 2") {
		t.Fatalf("view missing extension badges: %q", out)
	}
}

func TestDynamicAgentSlashCommandLoadsAgent(t *testing.T) {
	agent := &slashAgentStub{}
	model := tui.New(context.Background(), agent, false)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/fr")})
	model = next.(tui.Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)

	if agent.loadedAgent != "frontend" {
		t.Fatalf("loadedAgent = %q, want frontend", agent.loadedAgent)
	}

	out := model.View()
	if !strings.Contains(out, "Loaded custom agent: frontend") {
		t.Fatalf("view missing load confirmation: %q", out)
	}
}

func TestDynamicAgentSlashCommandCanForwardPrompt(t *testing.T) {
	agent := &slashAgentStub{}
	model := tui.New(context.Background(), agent, false)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/frontend Check README.md and review it")})
	model = next.(tui.Model)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)

	if cmd == nil {
		t.Fatal("expected slash agent with prompt to start a run")
	}
	if agent.loadedAgent != "frontend" {
		t.Fatalf("loadedAgent = %q, want frontend", agent.loadedAgent)
	}

	msg := cmd()
	next, _ = model.Update(msg)
	model = next.(tui.Model)

	if agent.lastPrompt != "Check README.md and review it" {
		t.Fatalf("lastPrompt = %q, want forwarded prompt", agent.lastPrompt)
	}
	if len(agent.loadedSkills) != 0 {
		t.Fatalf("loadedSkills = %v, want no auto-loaded skills", agent.loadedSkills)
	}
}

func TestSkillHashSyntaxActivatesSkillAndForwardsPrompt(t *testing.T) {
	agent := &slashAgentStub{}
	model := tui.New(context.Background(), agent, false)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("#do")})
	model = next.(tui.Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Improve the README quick start")})
	model = next.(tui.Model)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)

	if cmd == nil {
		t.Fatal("expected hash skill with prompt to start a run")
	}

	msg := cmd()
	next, _ = model.Update(msg)
	model = next.(tui.Model)

	if len(agent.loadedSkills) == 0 || agent.loadedSkills[0] != "docs" {
		t.Fatalf("loadedSkills = %v, want docs", agent.loadedSkills)
	}
	if strings.Contains(agent.lastPrompt, "#docs") {
		t.Fatalf("lastPrompt should not include hash tag, got %q", agent.lastPrompt)
	}
	if !strings.Contains(agent.lastPrompt, "Explicitly requested skills:\n- docs") {
		t.Fatalf("lastPrompt missing explicit skill note: %q", agent.lastPrompt)
	}
}

func TestModeSwitchClearsCustomAgent(t *testing.T) {
	agent := &modeSwitchAgentStub{mode: "chat", activeAgent: "reviewer.md"}
	model := tui.New(context.Background(), agent, false)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/coder")})
	model = next.(tui.Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)

	if agent.mode != "coder" {
		t.Fatalf("mode = %q, want coder", agent.mode)
	}
	if agent.activeAgent != "" {
		t.Fatalf("activeAgent = %q, want cleared", agent.activeAgent)
	}

	out := model.View()
	if strings.Contains(out, "reviewer.md") {
		t.Fatalf("view should not show cleared custom agent: %q", out)
	}
}

func TestCoderCompletedProgressShowsAgentOutputInTranscript(t *testing.T) {
	agent := &coderProgressAgentStub{mode: "coder"}
	model := tui.New(context.Background(), agent, false)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/runplan")})
	model = next.(tui.Model)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)

	if cmd == nil {
		t.Fatal("expected /runplan to start coder execution")
	}

	msg := cmd()
	for msg != nil {
		next, nextCmd := model.Update(msg)
		model = next.(tui.Model)
		if nextCmd == nil {
			break
		}
		msg = nextCmd()
	}

	out := model.View()
	if !strings.Contains(out, "Missing troubleshooting section") {
		t.Fatalf("view missing completed task output: %q", out)
	}
	if !strings.Contains(out, "reviewer") {
		t.Fatalf("view missing completed agent label: %q", out)
	}
}
