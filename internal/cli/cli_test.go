package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
)

func TestPickMode(t *testing.T) {
	cases := []struct {
		name       string
		arg        string
		piped      bool
		forceTUI   bool
		forceShell bool
		want       Mode
	}{
		{"arg only", "fix it", false, false, false, ModeOneShot},
		{"piped no arg", "", true, false, false, ModePipe},
		{"piped with arg", "summarize", true, false, false, ModePipe},
		{"nothing", "", false, false, false, ModeInteractive},
		{"force tui beats arg", "fix it", false, true, false, ModeInteractive},
		{"force shell beats nothing", "", false, false, true, ModeOneShot},
		{"force tui beats pipe", "", true, true, false, ModeInteractive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickMode(tc.arg, tc.piped, tc.forceTUI, tc.forceShell); got != tc.want {
				t.Fatalf("pickMode = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJoinPrompt(t *testing.T) {
	if got := joinPrompt("a", "b"); got != "a\n\nb" {
		t.Fatalf("joinPrompt(a,b) = %q", got)
	}
	if got := joinPrompt("", "b"); got != "b" {
		t.Fatalf("joinPrompt('',b) = %q", got)
	}
	if got := joinPrompt("a", ""); got != "a" {
		t.Fatalf("joinPrompt(a,'') = %q", got)
	}
}

func TestSplitRoots(t *testing.T) {
	got := splitRoots("  ")
	if len(got) != 0 {
		t.Fatalf("blank roots should yield none, got %v", got)
	}
	if got := splitRoots("a"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("single root parse failed: %v", got)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(`
# comment
APEX_PROVIDER=openai
OPENAI_API_KEY="sk-test"
export APEX_MODEL=gpt-4.1-mini
`), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	env := loadDotEnv(path)
	if env["APEX_PROVIDER"] != "openai" {
		t.Fatalf("provider = %q", env["APEX_PROVIDER"])
	}
	if env["OPENAI_API_KEY"] != "sk-test" {
		t.Fatalf("api key = %q", env["OPENAI_API_KEY"])
	}
	if env["APEX_MODEL"] != "gpt-4.1-mini" {
		t.Fatalf("model = %q", env["APEX_MODEL"])
	}
}

func TestLooksLikeWorkspaceEdit(t *testing.T) {
	if !looksLikeWorkspaceEdit("update .golangci.yml timeout to 5m") {
		t.Fatal("expected file edit prompt to be detected")
	}
	if looksLikeWorkspaceEdit("summarize the project architecture") {
		t.Fatal("non-edit prompt should not be detected")
	}
}

func TestShouldRetryWithToolNudge(t *testing.T) {
	messages := []domain.Message{{Role: domain.RoleUser, Content: "update .golangci.yml timeout to 5m"}}
	state := agent.LoopState{
		Turns:         []agent.Turn{{}},
		FinalResponse: &domain.Response{Message: domain.Message{Role: domain.RoleAssistant, Content: "done"}},
	}
	if !shouldRetryWithToolNudge(messages, state) {
		t.Fatal("expected nudge retry when no tools were used")
	}

	state.Turns[0].ToolCalls = []domain.ToolCall{{Name: "read_file"}}
	if shouldRetryWithToolNudge(messages, state) {
		t.Fatal("should not nudge when tools were already used")
	}
}
