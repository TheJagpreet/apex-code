package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Snapshot struct {
	Version    int                   `json:"version"`
	Model      string                `json:"model"`
	CWD        string                `json:"cwd"`
	WorkingSet contextmgr.WorkingSet `json:"working_set"`
}

type TurnRecord struct {
	Index            int            `json:"index"`
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	StopReason       string         `json:"stop_reason"`
	ToolCalls        int            `json:"tool_calls"`
	ToolResults      int            `json:"tool_results"`
	Error            string         `json:"error,omitempty"`
	CacheCreation    int            `json:"cache_creation_tokens,omitempty"`
	CacheRead        int            `json:"cache_read_tokens,omitempty"`
	SavedBy          map[string]int `json:"saved_by,omitempty"`
}

type SaveInput struct {
	SessionID   string
	Model       string
	CWD         string
	Prompt      string
	Termination string
	Snapshot    Snapshot
	Turns       []TurnRecord
}

type Record struct {
	ID          string
	Title       string
	Model       string
	CWD         string
	Prompt      string
	Termination string
	TurnCount   int
	CreatedAt   int64
	UpdatedAt   int64
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
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

func OpenMemory() (*Store, error) {
	return Open(":memory:")
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Init(ctx context.Context) error {
	pragmas := []string{
		`pragma busy_timeout = 5000`,
		`pragma journal_mode = wal`,
		`pragma synchronous = normal`,
	}
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}
	stmts := []string{
		`create table if not exists sessions (
			id text primary key,
			title text not null,
			model text not null,
			cwd text not null,
			prompt text not null,
			termination text not null,
			turn_count integer not null,
			snapshot_json text not null,
			created_at integer not null,
			updated_at integer not null
		)`,
		`create table if not exists session_turns (
			session_id text not null references sessions(id) on delete cascade,
			turn_index integer not null,
			prompt_tokens integer not null,
			completion_tokens integer not null,
			total_tokens integer not null,
			stop_reason text not null,
			tool_calls integer not null,
			tool_results integer not null,
			cache_creation_tokens integer not null,
			cache_read_tokens integer not null,
			error_text text not null,
			saved_by_json text not null,
			primary key(session_id, turn_index)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Save(ctx context.Context, in SaveInput) (Record, Snapshot, error) {
	if in.Snapshot.Version == 0 {
		in.Snapshot.Version = 1
	}
	if in.CWD != "" && in.Snapshot.CWD == "" {
		in.Snapshot.CWD = in.CWD
	}
	if in.Model != "" && in.Snapshot.Model == "" {
		in.Snapshot.Model = in.Model
	}
	if in.SessionID == "" {
		in.SessionID = uuid.NewString()
	}
	now := time.Now().UnixNano()
	record := Record{
		ID:          in.SessionID,
		Title:       summarizePrompt(in.Prompt),
		Model:       in.Snapshot.Model,
		CWD:         filepath.Clean(in.Snapshot.CWD),
		Prompt:      strings.TrimSpace(in.Prompt),
		Termination: in.Termination,
		TurnCount:   len(in.Turns),
		UpdatedAt:   now,
	}
	if record.Model == "" {
		record.Model = in.Model
	}
	if record.CWD == "." && in.CWD != "" {
		record.CWD = filepath.Clean(in.CWD)
	}

	snapJSON, err := json.Marshal(in.Snapshot)
	if err != nil {
		return Record{}, Snapshot{}, fmt.Errorf("marshal session snapshot: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, Snapshot{}, err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `select created_at from sessions where id = ?`, record.ID).Scan(&record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		record.CreatedAt = now
		err = nil
	}
	if err != nil {
		return Record{}, Snapshot{}, err
	}

	if _, err := tx.ExecContext(ctx, `insert into sessions(id, title, model, cwd, prompt, termination, turn_count, snapshot_json, created_at, updated_at)
		values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			title=excluded.title,
			model=excluded.model,
			cwd=excluded.cwd,
			prompt=excluded.prompt,
			termination=excluded.termination,
			turn_count=excluded.turn_count,
			snapshot_json=excluded.snapshot_json,
			updated_at=excluded.updated_at`,
		record.ID, record.Title, record.Model, record.CWD, record.Prompt, record.Termination, record.TurnCount, string(snapJSON), record.CreatedAt, record.UpdatedAt); err != nil {
		return Record{}, Snapshot{}, err
	}

	if _, err := tx.ExecContext(ctx, `delete from session_turns where session_id = ?`, record.ID); err != nil {
		return Record{}, Snapshot{}, err
	}
	for _, turn := range in.Turns {
		savedByJSON, err := json.Marshal(turn.SavedBy)
		if err != nil {
			return Record{}, Snapshot{}, fmt.Errorf("marshal turn savings: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `insert into session_turns(
			session_id, turn_index, prompt_tokens, completion_tokens, total_tokens, stop_reason,
			tool_calls, tool_results, cache_creation_tokens, cache_read_tokens, error_text, saved_by_json
		) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID, turn.Index, turn.PromptTokens, turn.CompletionTokens, turn.TotalTokens, turn.StopReason,
			turn.ToolCalls, turn.ToolResults, turn.CacheCreation, turn.CacheRead, turn.Error, string(savedByJSON)); err != nil {
			return Record{}, Snapshot{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Record{}, Snapshot{}, err
	}
	return record, in.Snapshot, nil
}

func (s *Store) Load(ctx context.Context, selector string) (Record, Snapshot, []TurnRecord, error) {
	row, err := s.loadSessionRow(ctx, selector)
	if err != nil {
		return Record{}, Snapshot{}, nil, err
	}
	record, snapJSON, err := scanSessionRow(row)
	if err != nil {
		return Record{}, Snapshot{}, nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(snapJSON), &snap); err != nil {
		return Record{}, Snapshot{}, nil, fmt.Errorf("decode session snapshot: %w", err)
	}
	turns, err := s.loadTurns(ctx, record.ID)
	if err != nil {
		return Record{}, Snapshot{}, nil, err
	}
	return record, snap, turns, nil
}

func (s *Store) List(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `select id, title, model, cwd, prompt, termination, turn_count, created_at, updated_at
		from sessions order by updated_at desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Title, &r.Model, &r.CWD, &r.Prompt, &r.Termination, &r.TurnCount, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) loadSessionRow(ctx context.Context, selector string) (*sql.Row, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || strings.EqualFold(selector, "latest") {
		return s.db.QueryRowContext(ctx, `select id, title, model, cwd, prompt, termination, turn_count, snapshot_json, created_at, updated_at
			from sessions order by updated_at desc limit 1`), nil
	}
	return s.db.QueryRowContext(ctx, `select id, title, model, cwd, prompt, termination, turn_count, snapshot_json, created_at, updated_at
		from sessions where id = ?`, selector), nil
}

func scanSessionRow(row *sql.Row) (Record, string, error) {
	var rec Record
	var snapshotJSON string
	err := row.Scan(&rec.ID, &rec.Title, &rec.Model, &rec.CWD, &rec.Prompt, &rec.Termination, &rec.TurnCount, &snapshotJSON, &rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, "", fmt.Errorf("session not found")
	}
	return rec, snapshotJSON, err
}

func (s *Store) loadTurns(ctx context.Context, sessionID string) ([]TurnRecord, error) {
	rows, err := s.db.QueryContext(ctx, `select turn_index, prompt_tokens, completion_tokens, total_tokens, stop_reason,
		tool_calls, tool_results, cache_creation_tokens, cache_read_tokens, error_text, saved_by_json
		from session_turns where session_id = ? order by turn_index`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TurnRecord
	for rows.Next() {
		var rec TurnRecord
		var savedByJSON string
		if err := rows.Scan(&rec.Index, &rec.PromptTokens, &rec.CompletionTokens, &rec.TotalTokens, &rec.StopReason,
			&rec.ToolCalls, &rec.ToolResults, &rec.CacheCreation, &rec.CacheRead, &rec.Error, &savedByJSON); err != nil {
			return nil, err
		}
		if strings.TrimSpace(savedByJSON) != "" {
			_ = json.Unmarshal([]byte(savedByJSON), &rec.SavedBy)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func summarizePrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(strings.TrimSpace(prompt)), " ")
	if prompt == "" {
		return "untitled session"
	}
	if len(prompt) > 72 {
		return prompt[:69] + "..."
	}
	return prompt
}
