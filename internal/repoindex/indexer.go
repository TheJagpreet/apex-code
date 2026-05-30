package repoindex

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/tokenizer"
)

type Indexer struct {
	root       string
	store      *Store
	parsers    *ParserRegistry
	embeddings EmbeddingOptions
}

func NewIndexer(root string, store *Store, parsers *ParserRegistry) *Indexer {
	if parsers == nil {
		parsers = NewParserRegistry()
	}
	return &Indexer{root: root, store: store, parsers: parsers}
}

func (i *Indexer) WithEmbeddings(opts EmbeddingOptions) *Indexer {
	i.embeddings = opts
	return i
}

func (i *Indexer) Index(ctx context.Context) (IndexStats, error) {
	files, err := WalkRepo(i.root)
	if err != nil {
		return IndexStats{}, err
	}
	stats := IndexStats{FilesSeen: len(files)}
	present := map[string]bool{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		present[file.Path] = true
		oldHash, ok, err := i.store.FileHash(ctx, file.Path)
		if err != nil {
			return stats, err
		}
		if ok && oldHash == file.Hash {
			stats.FilesSkipped++
			continue
		}
		symbols, err := i.parsers.ParseFile(ctx, file)
		if err != nil {
			return stats, fmt.Errorf("parse %s: %w", file.Path, err)
		}
		if i.embeddings.Enabled && i.embeddings.Embedder != nil && len(symbols) > 0 {
			texts := make([]string, 0, len(symbols))
			for _, sym := range symbols {
				texts = append(texts, sym.Signature+" "+sym.Doc)
			}
			if _, err := i.embeddings.Embedder.Embed(ctx, texts); err != nil {
				return stats, fmt.Errorf("embed %s: %w", file.Path, err)
			}
		}
		err = i.store.ReplaceFile(ctx, FileRecord{
			Path:      file.Path,
			Hash:      file.Hash,
			Language:  file.Language,
			Size:      file.Size,
			IndexedAt: time.Now().Unix(),
		}, symbols)
		if err != nil {
			return stats, err
		}
		stats.FilesIndexed++
		stats.SymbolsIndexed += len(symbols)
	}
	deleted, err := i.store.DeleteMissing(ctx, present)
	if err != nil {
		return stats, err
	}
	stats.FilesDeleted = deleted
	return stats, nil
}

type Retriever struct {
	root  string
	store *Store
}

func NewRetriever(root string, store *Store) *Retriever {
	return &Retriever{root: root, store: store}
}

func (r *Retriever) RepoMap(ctx context.Context, opts RepoMapOptions) (string, error) {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 600
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 40
	}
	symbols, err := r.store.Symbols(ctx)
	if err != nil {
		return "", err
	}
	byFile := map[string][]Symbol{}
	for _, sym := range symbols {
		byFile[sym.FilePath] = append(byFile[sym.FilePath], sym)
	}
	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)
	if len(files) > opts.MaxFiles {
		files = files[:opts.MaxFiles]
	}

	counter := tokenizer.NewHeuristic()
	lines := []string{"repo map:"}
	for _, file := range files {
		candidate := append([]string{}, lines...)
		candidate = append(candidate, file)
		for _, sym := range byFile[file] {
			candidate = append(candidate, compactSymbolLine(sym))
		}
		tokens, err := counter.Count(strings.Join(candidate, "\n"))
		if err != nil {
			return "", err
		}
		if tokens > opts.MaxTokens {
			lines = append(lines, "truncated: repo map budget reached")
			break
		}
		lines = candidate
	}
	return strings.Join(lines, "\n"), nil
}

func (r *Retriever) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 12
	}
	results, err := r.store.Search(ctx, quoteFTS(query), limit)
	if err != nil || len(results) == 0 {
		results, err = r.store.SearchLike(ctx, query, limit)
	}
	if err != nil {
		return nil, err
	}
	if opts.OutlineOnly {
		for i := range results {
			results[i].Doc = compactOneLine(results[i].Doc, 140)
		}
	}
	return results, nil
}

func (r *Retriever) Outline(ctx context.Context, query string, limit int) (string, error) {
	results, err := r.Search(ctx, query, SearchOptions{Limit: limit, OutlineOnly: true})
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(results)+1)
	lines = append(lines, "retrieval outline:")
	for _, result := range results {
		line := fmt.Sprintf("%s:%d-%d %s %s", result.FilePath, result.StartLine, result.EndLine, result.Kind, result.Signature)
		if result.Doc != "" {
			line += " doc=" + result.Doc
		}
		lines = append(lines, compactOneLine(line, 260))
	}
	if len(results) == 0 {
		lines = append(lines, "no matches")
	}
	return strings.Join(lines, "\n"), nil
}

func (r *Retriever) Range(ctx context.Context, req RangeRequest) (string, error) {
	if strings.TrimSpace(req.Path) == "" {
		return "", fmt.Errorf("path is required")
	}
	path := filepath.Join(r.root, filepath.FromSlash(req.Path))
	start := req.StartLine
	if start <= 0 {
		start = 1
	}
	end := req.EndLine
	if end > 0 && end < start {
		return "", fmt.Errorf("end_line must be >= start_line")
	}
	text, err := readLineRange(path, start, end)
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines)+1)
	out = append(out, fmt.Sprintf("%s:%d-%d", req.Path, start, start+len(lines)-1))
	for i, line := range lines {
		out = append(out, fmt.Sprintf("%d: %s", start+i, line))
	}
	return strings.Join(out, "\n"), nil
}

func compactSymbolLine(sym Symbol) string {
	line := fmt.Sprintf("  %d-%d %s %s", sym.StartLine, sym.EndLine, sym.Kind, sym.Signature)
	if sym.Doc != "" {
		line += " doc=" + sym.Doc
	}
	return compactOneLine(line, 260)
}

func compactOneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit > 0 && len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}
