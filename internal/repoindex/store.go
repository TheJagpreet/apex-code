package repoindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.Init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func OpenMemoryStore() (*Store, error) {
	return OpenStore(":memory:")
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Init(ctx context.Context) error {
	stmts := []string{
		`create table if not exists files (
			path text primary key,
			hash text not null,
			language text not null,
			size integer not null,
			indexed_at integer not null
		)`,
		`create table if not exists symbols (
			id integer primary key autoincrement,
			file_path text not null references files(path) on delete cascade,
			name text not null,
			kind text not null,
			signature text not null,
			doc text not null,
			start_line integer not null,
			end_line integer not null,
			language text not null
		)`,
		`create virtual table if not exists symbols_fts using fts5(
			file_path unindexed,
			name,
			kind,
			signature,
			doc,
			language unindexed
		)`,
		`create index if not exists symbols_file_idx on symbols(file_path)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) FileHash(ctx context.Context, path string) (string, bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `select hash from files where path = ?`, path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

func (s *Store) ReplaceFile(ctx context.Context, file FileRecord, symbols []Symbol) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `delete from symbols where file_path = ?`, file.Path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from symbols_fts where file_path = ?`, file.Path); err != nil {
		return err
	}
	if file.IndexedAt == 0 {
		file.IndexedAt = time.Now().Unix()
	}
	if _, err := tx.ExecContext(ctx, `insert into files(path, hash, language, size, indexed_at)
		values(?, ?, ?, ?, ?)
		on conflict(path) do update set hash=excluded.hash, language=excluded.language, size=excluded.size, indexed_at=excluded.indexed_at`,
		file.Path, file.Hash, file.Language, file.Size, file.IndexedAt); err != nil {
		return err
	}
	for _, sym := range symbols {
		if _, err := tx.ExecContext(ctx, `insert into symbols(file_path, name, kind, signature, doc, start_line, end_line, language)
			values(?, ?, ?, ?, ?, ?, ?, ?)`,
			sym.FilePath, sym.Name, string(sym.Kind), sym.Signature, sym.Doc, sym.StartLine, sym.EndLine, sym.Language); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into symbols_fts(file_path, name, kind, signature, doc, language)
			values(?, ?, ?, ?, ?, ?)`,
			sym.FilePath, sym.Name, string(sym.Kind), sym.Signature, sym.Doc, sym.Language); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteMissing(ctx context.Context, present map[string]bool) (int, error) {
	rows, err := s.db.QueryContext(ctx, `select path from files`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var missing []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return 0, err
		}
		if !present[path] {
			missing = append(missing, path)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, path := range missing {
		if _, err := s.db.ExecContext(ctx, `delete from files where path = ?`, path); err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `delete from symbols where file_path = ?`, path); err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `delete from symbols_fts where file_path = ?`, path); err != nil {
			return 0, err
		}
	}
	return len(missing), nil
}

func (s *Store) Symbols(ctx context.Context) ([]Symbol, error) {
	rows, err := s.db.QueryContext(ctx, `select file_path, name, kind, signature, doc, start_line, end_line, language
		from symbols order by file_path, start_line, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 12
	}
	rows, err := s.db.QueryContext(ctx, `select s.file_path, s.name, s.kind, s.signature, s.doc, s.start_line, s.end_line, s.language,
		bm25(symbols_fts) as score
		from symbols_fts
		join symbols s on s.file_path = symbols_fts.file_path and s.name = symbols_fts.name and s.signature = symbols_fts.signature
		where symbols_fts match ?
		order by score
		limit ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]SearchResult, 0)
	for rows.Next() {
		var r SearchResult
		var kind string
		if err := rows.Scan(&r.FilePath, &r.Name, &kind, &r.Signature, &r.Doc, &r.StartLine, &r.EndLine, &r.Language, &r.Score); err != nil {
			return nil, err
		}
		r.Kind = SymbolKind(kind)
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) SearchLike(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 12
	}
	pattern := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `select file_path, name, kind, signature, doc, start_line, end_line, language
		from symbols
		where name like ? or signature like ? or doc like ? or file_path like ?
		order by case when name like ? then 0 else 1 end, file_path, start_line
		limit ?`, pattern, pattern, pattern, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	symbols, err := scanSymbols(rows)
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(symbols))
	for i, sym := range symbols {
		results = append(results, SearchResult{
			FilePath:  sym.FilePath,
			Name:      sym.Name,
			Kind:      sym.Kind,
			Signature: sym.Signature,
			Doc:       sym.Doc,
			StartLine: sym.StartLine,
			EndLine:   sym.EndLine,
			Language:  sym.Language,
			Score:     float64(len(symbols) - i),
		})
	}
	return results, nil
}

func scanSymbols(rows *sql.Rows) ([]Symbol, error) {
	symbols := make([]Symbol, 0)
	for rows.Next() {
		var sym Symbol
		var kind string
		if err := rows.Scan(&sym.FilePath, &sym.Name, &kind, &sym.Signature, &sym.Doc, &sym.StartLine, &sym.EndLine, &sym.Language); err != nil {
			return nil, err
		}
		sym.Kind = SymbolKind(kind)
		symbols = append(symbols, sym)
	}
	return symbols, rows.Err()
}

func (s *Store) Schema() string {
	return "files(path, hash, language, size, indexed_at); symbols(file_path, name, kind, signature, doc, start_line, end_line, language); symbols_fts(name, kind, signature, doc)"
}

func quoteFTS(query string) string {
	if query == "" {
		return ""
	}
	return fmt.Sprintf("%q", query)
}
