package diffengine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/diffengine"
)

func TestApplyExactAnchor(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "hello world\n")
	res, err := diffengine.New(diffengine.Options{Root: dir}).Apply(context.Background(), diffengine.Patch{
		Files: []diffengine.FilePatch{{Path: "a.txt", Hunks: []diffengine.Hunk{{Old: "world", New: "apex"}}}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.HunksApplied != 1 || read(t, dir, "a.txt") != "hello apex\n" {
		t.Fatalf("res=%+v file=%q", res, read(t, dir, "a.txt"))
	}
}

func TestApplyFuzzyWhitespaceFallback(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "func main() {\n\tfmt.Println(\"hi\")\n}\n")
	_, err := diffengine.New(diffengine.Options{Root: dir}).Apply(context.Background(), diffengine.Patch{
		Files: []diffengine.FilePatch{{Path: "a.txt", Hunks: []diffengine.Hunk{{
			Old: "func main() {\n    fmt.Println(\"hi\")\n}",
			New: "func main() {\n\tfmt.Println(\"bye\")\n}",
		}}}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(read(t, dir, "a.txt"), "bye") {
		t.Fatalf("file = %q", read(t, dir, "a.txt"))
	}
}

func TestApplyStripsReadFileLineNumbersFromHunk(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "cfg.yml", "run:\n  timeout: 3m\n\nlinters:\n  enable:\n    - errcheck\n")
	_, err := diffengine.New(diffengine.Options{Root: dir}).Apply(context.Background(), diffengine.Patch{
		Files: []diffengine.FilePatch{{
			Path: "cfg.yml",
			Hunks: []diffengine.Hunk{{
				Old:          "1: run:\n2:   timeout: 3m",
				New:          "1: run:\n2:   timeout: 5m",
				AnchorAfter:  "4: linters:",
				AnchorBefore: "",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(read(t, dir, "cfg.yml"), "timeout: 5m") {
		t.Fatalf("file = %q", read(t, dir, "cfg.yml"))
	}
}

func TestAmbiguousAnchorRequestsReanchor(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "same\nsame\n")
	_, err := diffengine.New(diffengine.Options{Root: dir}).Apply(context.Background(), diffengine.Patch{
		Files: []diffengine.FilePatch{{Path: "a.txt", Hunks: []diffengine.Hunk{{Old: "same", New: "other"}}}},
	})
	if !errors.Is(err, diffengine.ErrAmbiguous) || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("err = %v", err)
	}
}

func TestAtomicRollbackOnFailure(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "one\n")
	write(t, dir, "b.txt", "two\n")
	res, err := diffengine.New(diffengine.Options{Root: dir}).Apply(context.Background(), diffengine.Patch{
		Files: []diffengine.FilePatch{
			{Path: "a.txt", Hunks: []diffengine.Hunk{{Old: "one", New: "changed"}}},
			{Path: "b.txt", Hunks: []diffengine.Hunk{{Old: "missing", New: "changed"}}},
		},
	})
	if err == nil || !res.RolledBack {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if read(t, dir, "a.txt") != "one\n" || read(t, dir, "b.txt") != "two\n" {
		t.Fatalf("rollback failed: a=%q b=%q", read(t, dir, "a.txt"), read(t, dir, "b.txt"))
	}
}

func TestVerificationFailureIsFilteredAndRolledBack(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "one\n")
	command := "echo noise && echo 'file.go:12: error: bad thing' && exit 2"
	if runtime.GOOS == "windows" {
		command = "Write-Output noise; Write-Output 'file.go:12: error: bad thing'; exit 2"
	}
	res, err := diffengine.New(diffengine.Options{Root: dir, OutputMaxLines: 2}).Apply(context.Background(), diffengine.Patch{
		Files:          []diffengine.FilePatch{{Path: "a.txt", Hunks: []diffengine.Hunk{{Old: "one", New: "changed"}}}},
		VerifyCommands: []string{command},
	})
	if err == nil || !res.RolledBack {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Contains(res.Verification.Output, "noise") || !strings.Contains(res.Verification.Output, "error") {
		t.Fatalf("filtered output = %q", res.Verification.Output)
	}
	if read(t, dir, "a.txt") != "one\n" {
		t.Fatalf("file = %q", read(t, dir, "a.txt"))
	}
}

func TestFailureFilterDedupesAndCaps(t *testing.T) {
	out := diffengine.FilterFailure(strings.Repeat("x.go:1: error: same\n", 20), 3, 80)
	if strings.Count(out, "same") != 1 || len(out) > 80 {
		t.Fatalf("out = %q", out)
	}
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
