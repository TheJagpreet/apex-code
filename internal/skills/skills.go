// Package skills implements apex-code's skill bundle discovery and lazy loading.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/apex-code/apex/internal/bundles"
	"github.com/apex-code/apex/internal/tools"
)

type Skill struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Triggers    []string `yaml:"triggers,omitempty"`
	Prompt      string   `yaml:"-"`
	Tools       []string `yaml:"tools,omitempty"`

	path string
}

func (s Skill) Path() string { return s.path }

func (s Skill) File() string { return filepath.Base(s.path) }

type Header struct {
	Name        string
	Description string
	Triggers    []string
	Path        string
}

func (h Header) File() string { return filepath.Base(h.Path) }

func (h Header) Line() string {
	return h.Name + ": " + oneLine(h.Description)
}

type Loader struct {
	roots   []string
	headers map[string]Header
	order   []string
	cache   map[string]Skill
}

func NewLoader(roots ...string) *Loader {
	return &Loader{
		roots:   roots,
		headers: map[string]Header{},
		cache:   map[string]Skill{},
	}
}

func (l *Loader) Discover() error {
	l.headers = map[string]Header{}
	l.order = nil
	l.cache = map[string]Skill{}
	for _, root := range l.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("discover skills in %s: %w", root, err)
		}
		for _, entry := range entries {
			path, ok := bundlePath(root, entry)
			if !ok {
				continue
			}
			hdr, err := readHeader(path)
			if err != nil || hdr.Name == "" {
				continue
			}
			if _, exists := l.headers[hdr.Name]; !exists {
				l.order = append(l.order, hdr.Name)
			}
			l.headers[hdr.Name] = hdr
		}
	}
	sort.Strings(l.order)
	return nil
}

func (l *Loader) Headers() []Header {
	out := make([]Header, 0, len(l.order))
	for _, name := range l.order {
		out = append(out, l.headers[name])
	}
	return out
}

func (l *Loader) Describe() string {
	headers := l.Headers()
	lines := make([]string, 0, len(headers))
	for _, hdr := range headers {
		lines = append(lines, hdr.Line())
	}
	return strings.Join(lines, "\n")
}

func (l *Loader) Load(name string) (Skill, error) {
	if skill, ok := l.cache[name]; ok {
		return skill, nil
	}
	hdr, ok := l.headers[name]
	if !ok {
		for _, candidate := range l.headers {
			if strings.EqualFold(candidate.Name, name) {
				hdr = candidate
				ok = true
				break
			}
		}
	}
	if !ok {
		return Skill{}, fmt.Errorf("skill %q not discovered", name)
	}
	skill, err := readBundle(hdr.Path)
	if err != nil {
		return Skill{}, err
	}
	l.cache[hdr.Name] = skill
	return skill, nil
}

func (l *Loader) Match(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lower := strings.ToLower(text)
	var matched []string
	for _, name := range l.order {
		hdr := l.headers[name]
		if strings.Contains(lower, strings.ToLower(name)) || triggerHit(lower, hdr.Description, hdr.Triggers) {
			matched = append(matched, name)
		}
	}
	return matched
}

func (l *Loader) Activate(name string, set *tools.LazySet) (Skill, error) {
	skill, err := l.Load(name)
	if err != nil {
		return Skill{}, err
	}
	if set != nil && len(skill.Tools) > 0 {
		set.Inject(skill.Tools...)
	}
	return skill, nil
}

func bundlePath(root string, entry os.DirEntry) (string, bool) {
	if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
		return filepath.Join(root, entry.Name()), true
	}
	return "", false
}

func readHeader(path string) (Header, error) {
	skill, err := readBundle(path)
	if err != nil {
		return Header{}, err
	}
	return Header{
		Name:        skill.Name,
		Description: skill.Description,
		Triggers:    append([]string(nil), skill.Triggers...),
		Path:        skill.path,
	}, nil
}

func readBundle(path string) (Skill, error) {
	var meta struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Triggers    []string `yaml:"triggers,omitempty"`
		Tools       []string `yaml:"tools,omitempty"`
	}
	body, err := bundles.ParseMarkdownBundle(path, &meta)
	if err != nil {
		return Skill{}, err
	}
	return Skill{
		Name:        strings.TrimSpace(meta.Name),
		Description: strings.TrimSpace(meta.Description),
		Triggers:    cleanStrings(meta.Triggers),
		Prompt:      body,
		Tools:       cleanStrings(meta.Tools),
		path:        path,
	}, nil
}

func triggerHit(loweredText, description string, triggers []string) bool {
	for _, phrase := range append([]string{description}, triggers...) {
		for _, word := range strings.Fields(strings.ToLower(phrase)) {
			word = strings.Trim(word, ".,:;!?\"'()[]{}")
			if len(word) < 4 {
				continue
			}
			if strings.Contains(loweredText, word) {
				return true
			}
		}
	}
	return false
}

func cleanStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
