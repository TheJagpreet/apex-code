package repoindex

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

var skippedDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, ".next": true, ".turbo": true, ".gocache": true,
	"coverage": true, "__pycache__": true,
}

type WalkedFile struct {
	Path     string
	AbsPath  string
	Hash     string
	Language string
	Size     int64
}

func WalkRepo(root string) ([]WalkedFile, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	matcher := loadGitIgnore(absRoot)
	files := make([]WalkedFile, 0)
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := d.Name()
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if skippedDirs[name] {
				return filepath.SkipDir
			}
			if matcher != nil && matcher.MatchesPath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if matcher != nil && matcher.MatchesPath(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 {
			return nil
		}
		lang := languageForPath(rel)
		if lang == "" {
			return nil
		}
		hash, binary, err := hashFile(path)
		if err != nil || binary {
			return nil
		}
		files = append(files, WalkedFile{
			Path:     rel,
			AbsPath:  path,
			Hash:     hash,
			Language: lang,
			Size:     info.Size(),
		})
		return nil
	})
	return files, err
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

func hashFile(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	var sample bytes.Buffer
	tee := io.TeeReader(f, &sample)
	h := sha256.New()
	if _, err := io.Copy(h, tee); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), isBinary(sample.Bytes()), nil
}

func isBinary(sample []byte) bool {
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	return bytes.IndexByte(sample, 0) >= 0
}

func languageForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".py":
		return "python"
	default:
		return ""
	}
}

func readLineRange(path string, start, end int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		if line < start {
			continue
		}
		if end > 0 && line > end {
			break
		}
		b.WriteString(scanner.Text())
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n"), scanner.Err()
}
