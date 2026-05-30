// Package skills implements apex-code's Skill bundle format and lazy
// discovery/loading (plan 8.4–8.5).
//
// A Skill is a small, self-contained bundle that augments the agent with: a
// prompt fragment (extra instructions), a named set of tools it expects, and a
// trigger description used to decide when it is relevant. Skills are discovered
// cheaply — only their name + trigger description are read up front — and the
// heavyweight prompt body is loaded on demand using the same defer mechanism
// as tool schemas (plan 8.5).
package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/apex-code/apex/internal/tools"
)

// BundleFile is the on-disk filename for a skill bundle inside a skill
// directory.
const BundleFile = "skill.json"

// Skill is a fully-loaded skill bundle (plan 8.4).
type Skill struct {
	// Name uniquely identifies the skill.
	Name string `json:"name"`
	// Description is the trigger description: a one-line summary used to decide
	// relevance. Kept cheap so it can be advertised without loading the body.
	Description string `json:"description"`
	// Prompt is the prompt fragment injected when the skill activates. This is
	// the heavyweight field that lazy discovery avoids reading up front.
	Prompt string `json:"prompt"`
	// Tools names the tools this skill expects to have available. They are
	// injected through the same lazy tool mechanism (plan 8.5).
	Tools []string `json:"tools,omitempty"`

	dir string
}

// Dir returns the directory the skill was loaded from (empty for in-memory
// skills).
func (s Skill) Dir() string { return s.dir }

// Header is the cheap, deferred view of a skill: name + trigger description and
// where to find it. Discovery returns these without reading prompt bodies.
type Header struct {
	Name        string
	Description string
	Path        string
}

// Line renders a header as a single prompt line, mirroring tool descriptors.
func (h Header) Line() string {
	return h.Name + ": " + oneLine(h.Description)
}

// Loader discovers and lazily loads skill bundles from one or more roots.
type Loader struct {
	roots   []string
	headers map[string]Header
	order   []string
	cache   map[string]Skill
}

// NewLoader builds a loader over the given root directories.
func NewLoader(roots ...string) *Loader {
	return &Loader{
		roots:   roots,
		headers: map[string]Header{},
		cache:   map[string]Skill{},
	}
}

// Discover walks the roots and records each skill's header (name + trigger
// description) without loading prompt bodies (plan 8.5). It is idempotent and
// may be called again to pick up new bundles.
func (l *Loader) Discover() error {
	l.headers = map[string]Header{}
	l.order = nil
	for _, root := range l.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("discover skills in %s: %w", root, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name(), BundleFile)
			hdr, err := readHeader(path)
			if err != nil {
				// A malformed bundle should not abort discovery of the rest.
				continue
			}
			if hdr.Name == "" {
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

// Headers returns the deferred catalogue of discovered skills.
func (l *Loader) Headers() []Header {
	out := make([]Header, 0, len(l.order))
	for _, name := range l.order {
		out = append(out, l.headers[name])
	}
	return out
}

// Describe renders the deferred skill catalogue as prompt lines.
func (l *Loader) Describe() string {
	hdrs := l.Headers()
	lines := make([]string, 0, len(hdrs))
	for _, h := range hdrs {
		lines = append(lines, h.Line())
	}
	return strings.Join(lines, "\n")
}

// Load reads and caches the full bundle for a discovered skill (plan 8.5).
func (l *Loader) Load(name string) (Skill, error) {
	if sk, ok := l.cache[name]; ok {
		return sk, nil
	}
	hdr, ok := l.headers[name]
	if !ok {
		return Skill{}, fmt.Errorf("skill %q not discovered", name)
	}
	sk, err := readBundle(hdr.Path)
	if err != nil {
		return Skill{}, err
	}
	l.cache[name] = sk
	return sk, nil
}

// Match returns the names of skills whose trigger words appear in the given
// text, as the lightweight relevance step before loading a body. Matching is
// deterministic Go work (word overlap), never a model round trip.
func (l *Loader) Match(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lower := strings.ToLower(text)
	var matched []string
	for _, name := range l.order {
		hdr := l.headers[name]
		if strings.Contains(lower, strings.ToLower(name)) || triggerHit(lower, hdr.Description) {
			matched = append(matched, name)
		}
	}
	return matched
}

// Activate loads a skill and injects its tools into the supplied lazy set so
// the model can immediately call them (plan 8.5). It returns the loaded skill.
func (l *Loader) Activate(name string, set *tools.LazySet) (Skill, error) {
	sk, err := l.Load(name)
	if err != nil {
		return Skill{}, err
	}
	if set != nil && len(sk.Tools) > 0 {
		set.Inject(sk.Tools...)
	}
	return sk, nil
}

func readHeader(path string) (Header, error) {
	sk, err := readBundle(path)
	if err != nil {
		return Header{}, err
	}
	return Header{Name: sk.Name, Description: sk.Description, Path: path}, nil
}

func readBundle(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	var sk Skill
	if err := json.Unmarshal(data, &sk); err != nil {
		return Skill{}, fmt.Errorf("parse skill %s: %w", path, err)
	}
	sk.Name = strings.TrimSpace(sk.Name)
	sk.dir = filepath.Dir(path)
	return sk, nil
}

// triggerHit reports whether any significant word from the trigger description
// appears in text.
func triggerHit(loweredText, description string) bool {
	for _, w := range strings.Fields(strings.ToLower(description)) {
		w = strings.Trim(w, ".,:;!?\"'()")
		if len(w) < 4 {
			continue
		}
		if strings.Contains(loweredText, w) {
			return true
		}
	}
	return false
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
