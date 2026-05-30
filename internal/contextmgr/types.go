// Package contextmgr curates the model window from a working set instead of
// replaying an append-only transcript.
package contextmgr

import (
	"time"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
)

type ItemKind string

const (
	ItemSystem       ItemKind = "system"
	ItemHistory      ItemKind = "history"
	ItemSummary      ItemKind = "summary"
	ItemPinnedFile   ItemKind = "pinned-file"
	ItemRetrieved    ItemKind = "retrieved-context"
	ItemToolResult   ItemKind = "tool-result"
	ItemTaskState    ItemKind = "task-state"
	ItemScratchState ItemKind = "scratch-state"
)

type Source string

const (
	SourceMessage Source = "message"
	SourceTool    Source = "tool"
	SourceFile    Source = "file"
	SourceState   Source = "state"
	SourceSummary Source = "summary"
)

type Metadata struct {
	ID        string
	Kind      ItemKind
	Pool      agent.PoolName
	Source    Source
	Path      string
	LastUsed  time.Time
	TokenSize int
	Pinned    bool
	Hash      string
	Stale     bool
}

type Item struct {
	Meta    Metadata
	Message domain.Message
	Digest  string
}

type WorkingSet struct {
	Items []Item
}

type Prompt struct {
	Messages []domain.Message
	Report   RenderReport
}

type RenderReport struct {
	ContextWindow int
	PromptLimit   int
	TokensByPool  map[agent.PoolName]int
	LimitsByPool  map[agent.PoolName]int
	TokensIn      int
	TokensOut     int
	TokensSaved   int
	SavedBy       map[string]int
	Evicted       []string
	Digested      []string
	Summarized    []string
	Elided        []string
}

type Options struct {
	MaxDigestChars int
	Logger         Instrumenter
}

type Instrumenter interface {
	Render(report RenderReport)
}

func DefaultOptions() Options {
	return Options{MaxDigestChars: 180}
}
