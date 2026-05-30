package tools

import (
	"sort"
	"strings"

	"github.com/apex-code/apex/internal/domain"
)

// Descriptor is the cheap, deferred representation of a tool: just its name and
// a one-line description. The system prompt lists these instead of full JSON
// schemas so that 20 tools cost ~20 lines rather than 20 schemas (plan 8.1).
type Descriptor struct {
	Name        string
	Description string
}

// Line renders a descriptor as a single "name: description" prompt line.
func (d Descriptor) Line() string {
	return d.Name + ": " + d.Description
}

// Descriptors returns the deferred name+one-line view of every registered tool,
// ordered deterministically. This is what goes into the system prompt by
// default; full schemas are injected on demand (plan 8.1).
func (r *Registry) Descriptors() []Descriptor {
	out := make([]Descriptor, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		out = append(out, Descriptor{
			Name:        tool.Name(),
			Description: oneLine(tool.Description()),
		})
	}
	return out
}

// DescribeDeferred renders the deferred tool catalogue as prompt lines.
func (r *Registry) DescribeDeferred() string {
	descs := r.Descriptors()
	lines := make([]string, 0, len(descs))
	for _, d := range descs {
		lines = append(lines, d.Line())
	}
	return strings.Join(lines, "\n")
}

// Router maps a model's stated intent (free text or explicit tool names) onto
// the full tool schemas that must be injected into the next turn (plan 8.2).
//
// Routing is deterministic Go work, never a model round trip: we scan the text
// for known tool names and return the matching full schemas.
type Router struct {
	registry *Registry
}

// NewRouter builds a router over a registry.
func NewRouter(registry *Registry) *Router {
	return &Router{registry: registry}
}

// Resolve returns the full schemas for the named tools, skipping unknown names
// and de-duplicating while preserving a stable (sorted) order.
func (r *Router) Resolve(names []string) []domain.ToolSpec {
	seen := map[string]bool{}
	picked := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if _, ok := r.registry.Lookup(name); !ok {
			continue
		}
		seen[name] = true
		picked = append(picked, name)
	}
	sort.Strings(picked)

	specs := make([]domain.ToolSpec, 0, len(picked))
	for _, name := range picked {
		tool, _ := r.registry.Lookup(name)
		specs = append(specs, domain.ToolSpec{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Schema(),
		})
	}
	return specs
}

// Route inspects free-form model text and returns the names of any registered
// tools it mentions. A tool counts as mentioned if its name appears as a
// whole word (case-insensitive). This is the lightweight intent step that lets
// the model "ask for" a tool before its schema is paid for (plan 8.2).
func (r *Router) Route(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lower := strings.ToLower(text)
	var matched []string
	for _, name := range r.registry.order {
		if containsWord(lower, strings.ToLower(name)) {
			matched = append(matched, name)
		}
	}
	return matched
}

// LazySet tracks which tool schemas are currently "live" (injected) across the
// agent loop. It starts empty — every tool is deferred — and grows as the
// router resolves new intents. Once a schema is injected it stays available so
// the model can keep calling it without re-routing (plan 8.3).
type LazySet struct {
	router   *Router
	injected map[string]bool
}

// NewLazySet builds an empty lazy set over a router.
func NewLazySet(router *Router) *LazySet {
	return &LazySet{router: router, injected: map[string]bool{}}
}

// Inject marks the named tools as live for subsequent turns. Unknown names are
// ignored. It reports whether anything new was injected.
func (s *LazySet) Inject(names ...string) bool {
	changed := false
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || s.injected[name] {
			continue
		}
		if _, ok := s.router.registry.Lookup(name); !ok {
			continue
		}
		s.injected[name] = true
		changed = true
	}
	return changed
}

// InjectFromText routes free model text to tool names and injects them. Returns
// the names newly injected this call.
func (s *LazySet) InjectFromText(text string) []string {
	var added []string
	for _, name := range s.router.Route(text) {
		if s.Inject(name) {
			added = append(added, name)
		}
	}
	return added
}

// Active returns the sorted names of all live tools.
func (s *LazySet) Active() []string {
	out := make([]string, 0, len(s.injected))
	for name := range s.injected {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Specs returns the full schemas for the live tools — this is what the agent
// loop drops into the next turn's tools pool (plan 8.3).
func (s *LazySet) Specs() []domain.ToolSpec {
	return s.router.Resolve(s.Active())
}

// SchemaTokens reports the eager vs lazy prompt-token cost of advertising the
// registry's tools, so the savings of deferred schemas can be measured across a
// task suite (plan 8.6).
//
//   - eager: every full JSON schema, as a cloud-first tool would send.
//   - lazy:  the deferred name+one-line catalogue plus any currently-injected
//     full schemas.
type SchemaTokens struct {
	Eager int
	Lazy  int
}

// Saved is the absolute token saving of the lazy approach.
func (t SchemaTokens) Saved() int { return t.Eager - t.Lazy }

// SavedRatio is the fraction of eager tokens avoided (0..1).
func (t SchemaTokens) SavedRatio() float64 {
	if t.Eager == 0 {
		return 0
	}
	return float64(t.Eager-t.Lazy) / float64(t.Eager)
}

// TokenCounter estimates the token cost of a string. Callers can pass a
// provider/tokenizer-backed counter; nil falls back to a ~4-chars-per-token
// heuristic so measurement never requires a live model.
type TokenCounter func(string) int

func heuristicCount(s string) int {
	if s == "" {
		return 0
	}
	return (len(strings.TrimSpace(s)) + 3) / 4
}

// MeasureSchemaTokens computes the eager/lazy schema cost for a registry given
// the set of currently-injected tools. Pass count=nil to use the heuristic
// counter (plan 8.6).
func MeasureSchemaTokens(count TokenCounter, r *Registry, injected []string) SchemaTokens {
	if count == nil {
		count = heuristicCount
	}

	eager := 0
	for _, tool := range r.List() {
		eager += count(tool.Name())
		eager += count(tool.Description())
		eager += count(string(tool.Schema()))
	}

	lazy := count(r.DescribeDeferred())
	live := map[string]bool{}
	for _, name := range injected {
		live[name] = true
	}
	for _, tool := range r.List() {
		if live[tool.Name()] {
			lazy += count(string(tool.Schema()))
		}
	}

	return SchemaTokens{Eager: eager, Lazy: lazy}
}

// containsWord reports whether needle appears in haystack delimited by
// non-alphanumeric boundaries (so "grep" does not match "greppish").
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	from := 0
	for {
		idx := strings.Index(haystack[from:], needle)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(needle)
		if isBoundary(haystack, start-1) && isBoundary(haystack, end) {
			return true
		}
		from = start + 1
		if from >= len(haystack) {
			return false
		}
	}
}

func isBoundary(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	c := s[i]
	switch {
	case c >= 'a' && c <= 'z':
		return false
	case c >= 'A' && c <= 'Z':
		return false
	case c >= '0' && c <= '9':
		return false
	default:
		return true
	}
}
