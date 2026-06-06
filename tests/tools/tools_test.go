package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/tools"
)

func TestRegistryDescribeAndDispatch(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	descriptions := reg.Describe()
	if len(descriptions) == 0 {
		t.Fatal("no registered tools")
	}

	dispatcher := tools.NewDispatcher(reg)
	results, err := dispatcher.DispatchToolCalls(context.Background(), []domain.ToolCall{
		{ID: "1", Name: "glob", Arguments: []byte(`{"pattern":"*.md"}`)},
	})
	if err != nil {
		t.Fatalf("DispatchToolCalls: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Content, "summary:") {
		t.Fatalf("results = %+v", results)
	}
	if strings.Contains(results[0].Content, "\ntokens: ") {
		t.Fatalf("tool content should not include token accounting: %+v", results[0])
	}
}

func TestReadFileToolRequiresRangeForLargeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", 5000)), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewReadFileTool(tools.NewGate(tools.DefaultGateOptions()))
	_, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{"path": path}))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadFileToolRangeOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewReadFileTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"path": path, "start_line": 2, "end_line": 3,
	}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, "2: two") || !strings.Contains(res.Payload, "3: three") {
		t.Fatalf("payload = %q", res.Payload)
	}
	if !strings.Contains(res.Payload, "sha256:") {
		t.Fatalf("payload missing sha256 header: %q", res.Payload)
	}
}

func TestListDirCollapsesHeavyDirs(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	os.Mkdir(filepath.Join(dir, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main"), 0o644)

	tool := tools.NewListDirTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{"path": dir}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, ".git/ (collapsed)") || !strings.Contains(res.Payload, "node_modules/ (collapsed)") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestGlobToolCappedSorted(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)

	tool := tools.NewGlobTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{"pattern": filepath.Join(dir, "*.txt"), "cap": 1}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, "a.txt") || !strings.Contains(res.Payload, "+1 more matches") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestGrepToolCompactContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("zero\nTODO first\nsecond\nTODO third\n"), 0o644)

	tool := tools.NewGrepTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"pattern":       "TODO",
		"path":          dir,
		"context_lines": 1,
		"cap":           1,
	}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, "TODO first") || !strings.Contains(res.Payload, "truncated") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestWriteFileAndEditTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")

	writeTool := tools.NewWriteFileTool(tools.NewGate(tools.DefaultGateOptions()))
	if _, err := writeTool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"path": path, "content": "hello world",
	})); err != nil {
		t.Fatalf("write Invoke: %v", err)
	}

	editTool := tools.NewEditTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := editTool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"path": path, "old": "world", "new": "apex",
	}))
	if err != nil {
		t.Fatalf("edit Invoke: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello apex" || !strings.Contains(res.Payload, "edited") {
		t.Fatalf("data = %q payload=%q", string(data), res.Payload)
	}
}

func TestWriteFileToolAllowsOverwriteWithoutHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte("timeout: 3m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTool := tools.NewWriteFileTool(tools.NewGate(tools.DefaultGateOptions()))
	if _, err := writeTool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"path": path, "content": "timeout: 5m\n",
	})); err != nil {
		t.Fatalf("write overwrite Invoke: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "timeout: 5m\n" {
		t.Fatalf("data = %q", string(data))
	}
}

func TestWriteFileToolHashMismatchReturnsCurrentSHA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte("timeout: 3m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTool := tools.NewWriteFileTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := writeTool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"path":            path,
		"content":         "timeout: 10m\n",
		"expected_sha256": strings.Repeat("a", 64),
	}))
	if err == nil {
		t.Fatal("expected hash mismatch")
	}
	if !strings.Contains(res.Payload, "current_sha256:") || !strings.Contains(res.Payload, "expected_sha256:") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestEditToolHashMismatchReturnsCurrentSHA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	editTool := tools.NewEditTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := editTool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"path":            path,
		"old":             "world",
		"new":             "apex",
		"expected_sha256": strings.Repeat("b", 64),
	}))
	if err == nil {
		t.Fatal("expected hash mismatch")
	}
	if !strings.Contains(res.Payload, "current_sha256:") || !strings.Contains(res.Payload, "expected_sha256:") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestEditToolCompactVerificationFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := "echo 'a.go:1: error: nope'; exit 2"
	if runtime.GOOS == "windows" {
		command = "Write-Output 'a.go:1: error: nope'; exit 2"
	}
	editTool := tools.NewEditTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := editTool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"files": []map[string]any{{
			"path": path,
			"hunks": []map[string]any{{
				"old": "world",
				"new": "apex",
			}},
		}},
		"verify_commands": []string{command},
	}))
	if err == nil {
		t.Fatal("expected verification error")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "hello world" {
		t.Fatalf("rollback failed: %q", string(body))
	}
	if !strings.Contains(res.Payload, "rolled_back: true") || !strings.Contains(res.Payload, "error") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestDispatcherPreservesCompactToolFailurePayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := "echo 'a.go:1: error: nope'; exit 2"
	if runtime.GOOS == "windows" {
		command = "Write-Output 'a.go:1: error: nope'; exit 2"
	}
	reg := tools.NewDefaultRegistry()
	results, err := tools.NewDispatcher(reg).DispatchToolCalls(context.Background(), []domain.ToolCall{{
		ID:   "edit_1",
		Name: "edit",
		Arguments: mustJSON(t, map[string]any{
			"files": []map[string]any{{
				"path": path,
				"hunks": []map[string]any{{
					"old": "world",
					"new": "apex",
				}},
			}},
			"verify_commands": []string{command},
		}),
	}})
	if err != nil {
		t.Fatalf("DispatchToolCalls: %v", err)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %+v", results)
	}
	if !strings.Contains(results[0].Content, "failure_output") || !strings.Contains(results[0].Content, "rolled_back: true") {
		t.Fatalf("content = %q", results[0].Content)
	}
}

func TestRunToolCompactOutput(t *testing.T) {
	tool := tools.NewRunTool(tools.NewGate(tools.DefaultGateOptions()))
	command := "echo 'hello from run'"
	if runtime.GOOS == "windows" {
		command = "Write-Output 'hello from run'"
	}
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"command": command,
	}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, "exit_code: 0") || !strings.Contains(res.Payload, "hello from run") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestFetchWebToolExtractsPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `<html><body><h1>Title</h1><p>Hello fetch tool</p></body></html>`)
	}))
	defer srv.Close()

	tool := tools.NewFetchWebTool(tools.NewGate(tools.DefaultGateOptions()), srv.Client())
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, "Hello fetch tool") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestFetchRawToolReturnsExactTextBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "# Heading\n\nliteral body")
	}))
	defer srv.Close()

	tool := tools.NewFetchRawTool(tools.NewGate(tools.DefaultGateOptions()), srv.Client())
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, "# Heading") || !strings.Contains(res.Payload, "literal body") {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestFetchJSONToolPrettyPrintsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"name":"toon","items":[1,2]}`)
	}))
	defer srv.Close()

	tool := tools.NewFetchJSONTool(tools.NewGate(tools.DefaultGateOptions()), srv.Client())
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Payload, `"name": "toon"`) || !strings.Contains(res.Payload, `"items": [`) {
		t.Fatalf("payload = %q", res.Payload)
	}
}

func TestCloneRepoToolClonesLocalRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit(source, "init")
	runGit(source, "config", "user.email", "test@example.com")
	runGit(source, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello repo"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(source, "add", "README.md")
	runGit(source, "commit", "-m", "init")

	tool := tools.NewCloneRepoTool(tools.NewGate(tools.DefaultGateOptions()))
	res, err := tool.Invoke(context.Background(), mustJSON(t, map[string]any{
		"repo_url": source,
		"path":     target,
	}))
	if err != nil {
		t.Fatalf("Invoke: %v payload=%q", err, res.Payload)
	}
	if _, err := os.Stat(filepath.Join(target, "README.md")); err != nil {
		t.Fatalf("expected cloned README.md: %v", err)
	}
}

func TestGateCompactsOutput(t *testing.T) {
	gate := tools.NewGate(tools.GateOptions{MaxChars: 40, MaxLines: 3, TailChars: 10, SummaryMaxLen: 20})
	res := gate.Apply(tools.Result{
		Payload: strings.Repeat("line\n", 20),
		Summary: strings.Repeat("summary ", 10),
	})
	if !res.Truncated || len(res.Summary) > 20 {
		t.Fatalf("result = %+v", res)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}
