package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/apex-code/apex/internal/bundles"
)

type Agent struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Aliases     []string `yaml:"aliases,omitempty"`

	Prompt string
	path   string
}

func (a Agent) Path() string { return a.path }

func (a Agent) File() string { return filepath.Base(a.path) }

type Header struct {
	Name        string
	Description string
	Aliases     []string
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
	cache   map[string]Agent
}

func NewLoader(roots ...string) *Loader {
	return &Loader{
		roots:   roots,
		headers: map[string]Header{},
		cache:   map[string]Agent{},
	}
}

func (l *Loader) Discover() error {
	l.headers = map[string]Header{}
	l.order = nil
	l.cache = map[string]Agent{}
	for _, root := range l.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("discover agents in %s: %w", root, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				continue
			}
			path := filepath.Join(root, entry.Name())
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

func (l *Loader) Load(name string) (Agent, error) {
	if agent, ok := l.cache[name]; ok {
		return agent, nil
	}
	hdr, ok := l.lookup(name)
	if !ok {
		return Agent{}, fmt.Errorf("agent %q not discovered", name)
	}
	agent, err := readBundle(hdr.Path)
	if err != nil {
		return Agent{}, err
	}
	l.cache[hdr.Name] = agent
	return agent, nil
}

func (l *Loader) lookup(name string) (Header, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Header{}, false
	}
	if hdr, ok := l.headers[name]; ok {
		return hdr, true
	}
	lower := strings.ToLower(name)
	for _, hdr := range l.headers {
		if strings.EqualFold(hdr.Name, name) {
			return hdr, true
		}
		for _, alias := range hdr.Aliases {
			if strings.EqualFold(alias, lower) || strings.EqualFold(alias, name) {
				return hdr, true
			}
		}
	}
	return Header{}, false
}

func readHeader(path string) (Header, error) {
	agent, err := readBundle(path)
	if err != nil {
		return Header{}, err
	}
	return Header{
		Name:        agent.Name,
		Description: agent.Description,
		Aliases:     append([]string(nil), agent.Aliases...),
		Path:        path,
	}, nil
}

func readBundle(path string) (Agent, error) {
	var meta struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Aliases     []string `yaml:"aliases,omitempty"`
	}
	body, err := bundles.ParseMarkdownBundle(path, &meta)
	if err != nil {
		return Agent{}, err
	}
	return Agent{
		Name:        strings.TrimSpace(meta.Name),
		Description: strings.TrimSpace(meta.Description),
		Aliases:     cleanStrings(meta.Aliases),
		Prompt:      body,
		path:        path,
	}, nil
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
