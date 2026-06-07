package bundles

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseMarkdownBundle reads a markdown bundle with YAML front matter and
// unmarshals the metadata into meta. The markdown body is returned separately.
func ParseMarkdownBundle(path string, meta any) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	frontMatter, body, ok := splitFrontMatter(string(data))
	if !ok {
		return "", fmt.Errorf("markdown bundle %s is missing YAML front matter", path)
	}
	if err := yaml.Unmarshal([]byte(frontMatter), meta); err != nil {
		return "", fmt.Errorf("parse front matter %s: %w", path, err)
	}
	return strings.TrimSpace(body), nil
}

func splitFrontMatter(text string) (frontMatter, body string, ok bool) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "", false
	}
	rest := strings.TrimPrefix(text, "---\n")
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", "", false
	}
	frontMatter = strings.TrimSpace(rest[:end])
	body = strings.TrimLeft(rest[end+len("\n---\n"):], "\n")
	return frontMatter, body, true
}
