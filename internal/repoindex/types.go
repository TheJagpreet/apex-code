// Package repoindex builds a compact, searchable map of a repository.
package repoindex

import "context"

type FileRecord struct {
	Path      string
	Hash      string
	Language  string
	Size      int64
	IndexedAt int64
}

type SymbolKind string

const (
	SymbolFunction SymbolKind = "function"
	SymbolMethod   SymbolKind = "method"
	SymbolType     SymbolKind = "type"
	SymbolClass    SymbolKind = "class"
	SymbolVariable SymbolKind = "variable"
)

type Symbol struct {
	FilePath  string
	Name      string
	Kind      SymbolKind
	Signature string
	Doc       string
	StartLine int
	EndLine   int
	Language  string
}

type IndexStats struct {
	FilesSeen      int
	FilesIndexed   int
	FilesSkipped   int
	FilesDeleted   int
	SymbolsIndexed int
}

type RepoMapOptions struct {
	MaxTokens int
	MaxFiles  int
}

type SearchOptions struct {
	Limit       int
	OutlineOnly bool
}

type SearchResult struct {
	FilePath  string
	Name      string
	Kind      SymbolKind
	Signature string
	Doc       string
	StartLine int
	EndLine   int
	Language  string
	Score     float64
}

type RangeRequest struct {
	Path      string
	StartLine int
	EndLine   int
}

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type EmbeddingOptions struct {
	Enabled  bool
	Embedder Embedder
}
