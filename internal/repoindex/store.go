package repoindex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Store struct {
	path  string
	mem   bool
	mu    sync.RWMutex
	files map[string]FileRecord
	byFile map[string][]Symbol
}

type storeDocument struct {
	Files   []FileRecord `json:"files"`
	Symbols []Symbol     `json:"symbols"`
}

func OpenStore(path string) (*Store, error) {
	s := &Store{
		path:   path,
		mem:    path == "" || path == ":memory:",
		files:  map[string]FileRecord{},
		byFile: map[string][]Symbol{},
	}
	if !s.mem {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := s.load(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func OpenMemoryStore() (*Store, error) {
	return OpenStore(":memory:")
}

func (s *Store) Close() error { return nil }

func (s *Store) Init(_ context.Context) error { return nil }

func (s *Store) FileHash(_ context.Context, path string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, ok := s.files[path]
	if !ok {
		return "", false, nil
	}
	return file.Hash, true, nil
}

func (s *Store) ReplaceFile(_ context.Context, file FileRecord, symbols []Symbol) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[file.Path] = file
	cloned := make([]Symbol, 0, len(symbols))
	for _, sym := range symbols {
		cloned = append(cloned, sym)
	}
	s.byFile[file.Path] = cloned
	return s.persistLocked()
}

func (s *Store) DeleteMissing(_ context.Context, present map[string]bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for path := range s.files {
		if !present[path] {
			delete(s.files, path)
			delete(s.byFile, path)
			deleted++
		}
	}
	return deleted, s.persistLocked()
}

func (s *Store) Symbols(_ context.Context) ([]Symbol, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Symbol, 0)
	for _, symbols := range s.byFile {
		out = append(out, symbols...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FilePath == out[j].FilePath {
			if out[i].StartLine == out[j].StartLine {
				return out[i].Name < out[j].Name
			}
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].FilePath < out[j].FilePath
	})
	return out, nil
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	return s.searchLike(ctx, strings.Trim(query, `"`), limit)
}

func (s *Store) SearchLike(_ context.Context, query string, limit int) ([]SearchResult, error) {
	return s.searchLike(context.Background(), query, limit)
}

func (s *Store) searchLike(_ context.Context, query string, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 12
	}
	query = strings.ToLower(strings.TrimSpace(query))
	results := make([]SearchResult, 0)
	for _, symbols := range s.byFile {
		for _, sym := range symbols {
			haystack := strings.ToLower(strings.Join([]string{
				sym.FilePath, sym.Name, string(sym.Kind), sym.Signature, sym.Doc, sym.Language,
			}, " "))
			if query != "" && !strings.Contains(haystack, query) {
				continue
			}
			score := float64(matchScore(sym, query))
			results = append(results, SearchResult{
				FilePath:  sym.FilePath,
				Name:      sym.Name,
				Kind:      sym.Kind,
				Signature: sym.Signature,
				Doc:       sym.Doc,
				StartLine: sym.StartLine,
				EndLine:   sym.EndLine,
				Language:  sym.Language,
				Score:     score,
			})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			if results[i].FilePath == results[j].FilePath {
				return results[i].StartLine < results[j].StartLine
			}
			return results[i].FilePath < results[j].FilePath
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Store) Schema() string {
	return "files(path, hash, language, size, indexed_at); symbols(file_path, name, kind, signature, doc, start_line, end_line, language)"
}

func quoteFTS(query string) string {
	if query == "" {
		return ""
	}
	return fmt.Sprintf("%q", query)
}

func matchScore(sym Symbol, query string) int {
	score := 1
	lq := strings.ToLower(query)
	if strings.Contains(strings.ToLower(sym.Name), lq) {
		score += 8
	}
	if strings.Contains(strings.ToLower(sym.Signature), lq) {
		score += 4
	}
	if strings.Contains(strings.ToLower(sym.Doc), lq) {
		score += 2
	}
	if strings.Contains(strings.ToLower(sym.FilePath), lq) {
		score++
	}
	return score
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc storeDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode repo index store: %w", err)
	}
	for _, file := range doc.Files {
		s.files[file.Path] = file
	}
	for _, sym := range doc.Symbols {
		s.byFile[sym.FilePath] = append(s.byFile[sym.FilePath], sym)
	}
	return nil
}

func (s *Store) persistLocked() error {
	if s.mem || strings.TrimSpace(s.path) == "" {
		return nil
	}
	doc := storeDocument{
		Files:   make([]FileRecord, 0, len(s.files)),
		Symbols: make([]Symbol, 0),
	}
	for _, file := range s.files {
		doc.Files = append(doc.Files, file)
	}
	sort.SliceStable(doc.Files, func(i, j int) bool { return doc.Files[i].Path < doc.Files[j].Path })
	for _, file := range doc.Files {
		doc.Symbols = append(doc.Symbols, s.byFile[file.Path]...)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode repo index store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
