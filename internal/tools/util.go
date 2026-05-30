package tools

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultReadForceRangeBytes = 16 << 10
	defaultListCap             = 120
	defaultGlobCap             = 200
	defaultGrepCap             = 60
	defaultRunOutputChars      = 2400
)

var collapsedDirNames = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	".gocache":     {},
	"dist":         {},
	"build":        {},
}

func resolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, filepath.Clean(path)), nil
}

func formatLineRange(lines []string, start int) string {
	var b strings.Builder
	for i, line := range lines {
		b.WriteString(intToString(start + i))
		b.WriteString(": ")
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines := make([]string, 0)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func trimAndSort(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func intToString(n int) string {
	return strconv.Itoa(n)
}

func stripPathSigil(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "@") {
		return strings.TrimPrefix(path, "@")
	}
	return path
}
