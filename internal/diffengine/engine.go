package diffengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrAnchorNotFound = errors.New("diffengine: anchor not found")
	ErrAmbiguous      = errors.New("diffengine: ambiguous anchor")
	ErrHashMismatch   = errors.New("diffengine: file hash mismatch")
)

func New(opts Options) *Engine {
	if opts.Root == "" {
		opts.Root = "."
	}
	if opts.OutputMaxLines <= 0 {
		opts.OutputMaxLines = 40
	}
	if opts.OutputMaxChars <= 0 {
		opts.OutputMaxChars = 4000
	}
	return &Engine{opts: opts}
}

func (e *Engine) Apply(ctx context.Context, patch Patch) (Result, error) {
	if len(patch.Files) == 0 {
		return Result{}, fmt.Errorf("patch has no files")
	}
	format := e.opts.Format || patch.Format
	verifyHooks := append([]string(nil), e.opts.VerifyHooks...)
	verifyHooks = append(verifyHooks, patch.VerifyCommands...)

	backups := map[string][]byte{}
	result := Result{}
	applied := map[string]bool{}
	for _, filePatch := range patch.Files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path, err := e.resolve(filePatch.Path)
		if err != nil {
			return e.rollback(backups, result, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return e.rollback(backups, result, err)
		}
		if filePatch.ExpectedSHA256 != "" && sha256Hex(data) != filePatch.ExpectedSHA256 {
			return e.rollback(backups, result, ErrHashMismatch)
		}
		if _, ok := backups[path]; !ok {
			backups[path] = append([]byte(nil), data...)
		}
		updated := string(data)
		for _, hunk := range filePatch.Hunks {
			next, err := applyHunk(updated, hunk)
			if err != nil {
				return e.rollback(backups, result, fmt.Errorf("%s: %w", filePatch.Path, err))
			}
			updated = next
			result.HunksApplied++
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return e.rollback(backups, result, err)
		}
		if !applied[path] {
			result.AppliedFiles = append(result.AppliedFiles, filepath.ToSlash(path))
			applied[path] = true
		}
	}
	if format {
		for path := range applied {
			if filepath.Ext(path) == ".go" {
				verifyHooks = append([]string{"gofmt -w " + shellQuote(path)}, verifyHooks...)
				break
			}
		}
	}
	for _, hook := range verifyHooks {
		vr := runHook(ctx, hook, e.opts.Root)
		vr.Output = FilterFailure(vr.Output, e.opts.OutputMaxLines, e.opts.OutputMaxChars)
		result.Verification = vr
		if !vr.OK {
			return e.rollback(backups, result, fmt.Errorf("verification failed: %s", hook))
		}
	}
	result.Verification.OK = true
	return result, nil
}

func (e *Engine) rollback(backups map[string][]byte, result Result, cause error) (Result, error) {
	for path, data := range backups {
		_ = os.WriteFile(path, data, 0o644)
	}
	if len(backups) > 0 {
		result.RolledBack = true
	}
	return result, cause
}

func (e *Engine) resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	root, err := filepath.Abs(e.opts.Root)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(path)), nil
}

func applyHunk(text string, h Hunk) (string, error) {
	h = sanitizeHunk(h)
	if h.Old == "" {
		return "", fmt.Errorf("old block must not be empty")
	}
	if h.AnchorBefore != "" || h.AnchorAfter != "" {
		start := 0
		end := len(text)
		if h.AnchorBefore != "" {
			idx := strings.Index(text, h.AnchorBefore)
			if idx < 0 {
				return "", fmt.Errorf("%w: anchor_before not found", ErrAnchorNotFound)
			}
			start = idx + len(h.AnchorBefore)
		}
		if h.AnchorAfter != "" {
			idx := strings.Index(text[start:], h.AnchorAfter)
			if idx < 0 {
				return "", fmt.Errorf("%w: anchor_after not found", ErrAnchorNotFound)
			}
			end = start + idx
		}
		scoped, err := replaceUnique(text[start:end], h.Old, h.New)
		if err == nil {
			return text[:start] + scoped + text[end:], nil
		}
	}
	if updated, err := replaceUnique(text, h.Old, h.New); err == nil {
		return updated, nil
	} else if !errors.Is(err, ErrAnchorNotFound) {
		return "", err
	}
	return replaceFuzzy(text, h.Old, h.New)
}

func replaceUnique(text, old, new string) (string, error) {
	first := strings.Index(text, old)
	if first < 0 {
		return "", ErrAnchorNotFound
	}
	if strings.Index(text[first+len(old):], old) >= 0 {
		return "", fmt.Errorf("%w: old block appears more than once; add anchor_before/anchor_after", ErrAmbiguous)
	}
	return text[:first] + new + text[first+len(old):], nil
}

func replaceFuzzy(text, old, new string) (string, error) {
	lines := strings.Split(text, "\n")
	oldLines := strings.Split(old, "\n")
	want := normalizeBlock(old)
	matches := make([][2]int, 0)
	for i := 0; i+len(oldLines) <= len(lines); i++ {
		candidate := strings.Join(lines[i:i+len(oldLines)], "\n")
		if normalizeBlock(candidate) == want {
			matches = append(matches, [2]int{i, i + len(oldLines)})
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%w: exact and whitespace-tolerant matches failed", ErrAnchorNotFound)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("%w: fuzzy block appears more than once; add anchors", ErrAmbiguous)
	}
	m := matches[0]
	next := append([]string{}, lines[:m[0]]...)
	next = append(next, strings.Split(new, "\n")...)
	next = append(next, lines[m[1]:]...)
	return strings.Join(next, "\n"), nil
}

func normalizeBlock(text string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func sanitizeHunk(h Hunk) Hunk {
	h.Old = stripLineNumbers(h.Old)
	h.New = stripLineNumbers(h.New)
	h.AnchorBefore = stripLineNumbers(h.AnchorBefore)
	h.AnchorAfter = stripLineNumbers(h.AnchorAfter)
	return h
}

func stripLineNumbers(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		indent := line[:len(line)-len(trimmed)]
		j := 0
		for j < len(trimmed) && trimmed[j] >= '0' && trimmed[j] <= '9' {
			j++
		}
		if j > 0 && j+1 < len(trimmed) && trimmed[j] == ':' && trimmed[j+1] == ' ' {
			lines[i] = indent + trimmed[j+2:]
			changed = true
		}
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
