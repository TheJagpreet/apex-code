package tui_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/tui"
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
	if !strings.Contains(out, "agent frontend.md") || !strings.Contains(out, "tailwind.md+1") {
		t.Fatalf("view missing extension badges: %q", out)
	}
}
