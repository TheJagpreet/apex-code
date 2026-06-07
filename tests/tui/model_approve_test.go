package tui_test

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/tui"
)

type approveAgentStub struct {
	approveCalled chan struct{}
	executeCalled chan struct{}
}

func (a *approveAgentStub) Send(context.Context, string) (tui.Reply, error) { return tui.Reply{}, nil }
func (a *approveAgentStub) Stream(context.Context, string, func(string)) (tui.Reply, error) {
	return tui.Reply{}, nil
}
func (a *approveAgentStub) Model() string                               { return "deepseek-v4-flash" }
func (a *approveAgentStub) CWD() string                                 { return "repo" }
func (a *approveAgentStub) SessionLabel() string                        { return "session-1" }
func (a *approveAgentStub) LazyTools() bool                             { return false }
func (a *approveAgentStub) ResumeSession(context.Context, string) error { return nil }
func (a *approveAgentStub) NewSession() error                           { return nil }
func (a *approveAgentStub) SetModel(context.Context, string) error      { return nil }
func (a *approveAgentStub) ListSessions(context.Context, int) ([]tui.SessionOption, error) {
	return nil, nil
}
func (a *approveAgentStub) Mode() string                          { return "coder" }
func (a *approveAgentStub) SetMode(context.Context, string) error { return nil }
func (a *approveAgentStub) CoderSubmit(context.Context, string) (tui.Reply, error) {
	return tui.Reply{}, nil
}
func (a *approveAgentStub) CoderReview(context.Context, string) (tui.Reply, error) {
	return tui.Reply{}, nil
}
func (a *approveAgentStub) CoderApprove(context.Context) (tui.Reply, error) {
	select {
	case <-a.approveCalled:
	default:
		close(a.approveCalled)
	}
	return tui.Reply{Text: "approved"}, nil
}
func (a *approveAgentStub) CoderExecute(context.Context) (tui.Reply, error) { return tui.Reply{}, nil }
func (a *approveAgentStub) CoderExecuteStream(context.Context, func(tui.Reply)) (tui.Reply, error) {
	select {
	case <-a.executeCalled:
	default:
		close(a.executeCalled)
	}
	return tui.Reply{Text: "executed"}, nil
}
func (a *approveAgentStub) CoderWorkflow() *domain.CoderWorkflow          { return nil }
func (a *approveAgentStub) LiveStatus(context.Context) (tui.Reply, error) { return tui.Reply{}, nil }
func (a *approveAgentStub) Extensions() tui.ExtensionView                 { return tui.ExtensionView{} }
func (a *approveAgentStub) ReloadExtensions(context.Context) (tui.ExtensionView, error) {
	return tui.ExtensionView{}, nil
}
func (a *approveAgentStub) SetActiveAgent(context.Context, string) error { return nil }

func TestApproveCommandApprovesThenStreamsExecution(t *testing.T) {
	agent := &approveAgentStub{approveCalled: make(chan struct{}), executeCalled: make(chan struct{})}
	model := tui.New(context.Background(), agent, false)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/approve")})
	model = next.(tui.Model)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(tui.Model)

	if cmd == nil {
		t.Fatal("expected enter on /approve to start a command")
	}

	select {
	case <-agent.approveCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("CoderApprove was not called")
	}
	select {
	case <-agent.executeCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("CoderExecuteStream was not called")
	}

	msg := cmd()
	next, _ = model.Update(msg)
	_ = next.(tui.Model)
}
