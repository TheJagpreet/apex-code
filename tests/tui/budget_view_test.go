package tui_test

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/tui"
)

type budgetAgentStub struct{}

func (budgetAgentStub) Send(context.Context, string) (tui.Reply, error) { return tui.Reply{}, nil }
func (budgetAgentStub) Stream(context.Context, string, func(string)) (tui.Reply, error) {
	return tui.Reply{
		Text: "done",
		Budget: tui.BudgetSnapshot{
			PromptTokens:   1234,
			SessionTokens:  0,
			SessionTracked: true,
		},
	}, nil
}
func (budgetAgentStub) Model() string                                                  { return "deepseek-v4-flash" }
func (budgetAgentStub) CWD() string                                                    { return "repo" }
func (budgetAgentStub) SessionLabel() string                                           { return "session-1" }
func (budgetAgentStub) LazyTools() bool                                                { return false }
func (budgetAgentStub) ResumeSession(context.Context, string) error                    { return nil }
func (budgetAgentStub) NewSession() error                                              { return nil }
func (budgetAgentStub) SetModel(context.Context, string) error                         { return nil }
func (budgetAgentStub) ListSessions(context.Context, int) ([]tui.SessionOption, error) { return nil, nil }
func (budgetAgentStub) Mode() string                                                   { return "chat" }
func (budgetAgentStub) SetMode(context.Context, string) error                          { return nil }
func (budgetAgentStub) CoderSubmit(context.Context, string) (tui.Reply, error)        { return tui.Reply{}, nil }
func (budgetAgentStub) CoderReview(context.Context, string) (tui.Reply, error)        { return tui.Reply{}, nil }
func (budgetAgentStub) CoderApprove(context.Context) (tui.Reply, error)               { return tui.Reply{}, nil }
func (budgetAgentStub) CoderExecute(context.Context) (tui.Reply, error)               { return tui.Reply{}, nil }
func (budgetAgentStub) CoderExecuteStream(context.Context, func(tui.Reply)) (tui.Reply, error) {
	return tui.Reply{}, nil
}
func (budgetAgentStub) CoderWorkflow() *domain.CoderWorkflow     { return nil }
func (budgetAgentStub) LiveStatus(context.Context) (tui.Reply, error) { return tui.Reply{}, nil }

func TestViewShowsTrackedSessionTokensInHeader(t *testing.T) {
	model := tui.New(context.Background(), budgetAgentStub{}, false)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	model = next.(tui.Model)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)
	if cmd == nil {
		t.Fatal("expected enter to start a command")
	}

	msg := cmd()
	next, _ = model.Update(msg)
	model = next.(tui.Model)

	out := model.View()
	if !strings.Contains(out, "tok 0") {
		t.Fatalf("view missing tracked session tokens: %q", out)
	}
}
