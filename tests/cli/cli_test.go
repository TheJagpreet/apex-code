package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apex-code/apex/internal/cli"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/session"
	"github.com/apex-code/apex/internal/telemetry"
)

func TestMainStatsReadsTelemetryFromSessionsTree(t *testing.T) {
	t.Setenv("APEX_NO_OPEN_BROWSER", "1")
	root := t.TempDir()
	files, err := telemetry.OpenFileStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	if err := files.AppendEvent(context.Background(), "sess-1", telemetry.FileMeta{Model: "deepseek-v4-flash"}, telemetry.SessionEvent{
		Index:            1,
		Timestamp:        time.Unix(1000, 0).UTC(),
		Kind:             "llm_turn",
		Model:            "deepseek-v4-flash",
		PromptTokens:     12,
		CompletionTokens: 8,
		TotalTokens:      20,
		CustomAgent:      "reviewer",
		CustomAgentFile:  "reviewer.md",
		CustomSkills:     []string{"docs", "testing"},
		CustomSkillFiles: []string{"docs.md", "testing.md"},
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	stdout, stderr, exitCode := captureOutput(t, func() int {
		return cli.Main([]string{"stats", "-data-dir", root})
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "Opened stats dashboard:") {
		t.Fatalf("stats output = %q", stdout)
	}
	path := strings.TrimSpace(strings.TrimPrefix(stdout, "Opened stats dashboard:"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	if !strings.Contains(string(data), "Session Intelligence Dashboard") ||
		!strings.Contains(string(data), "sess-1") ||
		!strings.Contains(string(data), "Custom Agents") ||
		!strings.Contains(string(data), "Custom Skills") ||
		!strings.Contains(string(data), "reviewer.md") ||
		!strings.Contains(string(data), "docs.md") {
		t.Fatalf("dashboard html missing expected content: %s", string(data))
	}
}

func TestMainSessionsListsSessionRecordsFromSessionsTree(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(root)
	if err != nil {
		t.Fatalf("open session store: %v", err)
	}
	if _, _, err := store.Save(context.Background(), session.SaveInput{
		SessionID:   "sess-1",
		Model:       "deepseek-v4-flash",
		CWD:         filepath.Join(root, "repo"),
		Prompt:      "create arch doc",
		Termination: "final_answer",
		Snapshot:    session.Snapshot{Version: 1, Model: "deepseek-v4-flash", CWD: filepath.Join(root, "repo")},
		Turns:       []session.TurnRecord{{Index: 1, TotalTokens: 20}},
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	stdout, stderr, exitCode := captureOutput(t, func() int {
		return cli.Main([]string{"sessions", "-data-dir", root})
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "sess-1") {
		t.Fatalf("sessions output = %q", stdout)
	}
}

func TestMainTopLevelStatsFlagOpensDashboard(t *testing.T) {
	t.Setenv("APEX_NO_OPEN_BROWSER", "1")
	root := t.TempDir()
	files, err := telemetry.OpenFileStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	if err := files.AppendEvent(context.Background(), "sess-2", telemetry.FileMeta{Model: "deepseek-v4-flash"}, telemetry.SessionEvent{
		Index:       1,
		Timestamp:   time.Unix(1000, 0).UTC(),
		Kind:        "llm_turn",
		Model:       "deepseek-v4-flash",
		TotalTokens: 42,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	stdout, stderr, exitCode := captureOutput(t, func() int {
		return cli.Main([]string{"--stats", "-data-dir", root})
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "Opened stats dashboard:") {
		t.Fatalf("stats output = %q", stdout)
	}
}

func TestComposeCoderPromptIncludesRecentChatContext(t *testing.T) {
	got := cli.ComposeCoderPrompt("\x00\x00Create the files now", []domain.Message{
		{Role: domain.RoleSystem, Content: "system"},
		{Role: domain.RoleUser, Content: "We need a JSON to Toon converter."},
		{Role: domain.RoleAssistant, Content: "The Toon repo uses indentation for objects and table-like arrays."},
		{Role: domain.RoleTool, ToolResults: []domain.ToolResult{{ToolCallID: "1", Content: "tool"}}},
	})
	if strings.Contains(got, "\x00") {
		t.Fatalf("compose coder prompt should strip NUL bytes: %q", got)
	}
	if !strings.Contains(got, "Current request:\nCreate the files now") {
		t.Fatalf("compose coder prompt missing current request: %q", got)
	}
	if !strings.Contains(got, "Prior conversation context from this session:") {
		t.Fatalf("compose coder prompt missing context header: %q", got)
	}
	if !strings.Contains(got, "user: We need a JSON to Toon converter.") {
		t.Fatalf("compose coder prompt missing prior user context: %q", got)
	}
	if !strings.Contains(got, "assistant: The Toon repo uses indentation for objects and table-like arrays.") {
		t.Fatalf("compose coder prompt missing prior assistant context: %q", got)
	}
}

func captureOutput(t *testing.T, fn func() int) (string, string, int) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*.txt")
	if err != nil {
		t.Fatalf("stdout temp file: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr-*.txt")
	if err != nil {
		t.Fatalf("stderr temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	exitCode := fn()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	stdoutBytes, _ := os.ReadFile(stdoutFile.Name())
	stderrBytes, _ := os.ReadFile(stderrFile.Name())
	return string(stdoutBytes), string(stderrBytes), exitCode
}
