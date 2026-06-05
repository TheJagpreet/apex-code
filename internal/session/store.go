package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/google/uuid"
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

type sessionDocument struct {
	Record   Record      `json:"record"`
	Snapshot Snapshot    `json:"snapshot"`
	Turns    []TurnRecord `json:"turns"`
}

type Store struct {
	root string
}

func Open(path string) (*Store, error) {
	root := sessionRoot(path)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func OpenMemory() (*Store, error) {
	return Open(filepath.Join(os.TempDir(), "apex-session-memory-"+uuid.NewString()))
}

func (s *Store) Close() error { return nil }

func (s *Store) Init(_ context.Context) error {
	if s == nil {
		return nil
	}
	return os.MkdirAll(s.root, 0o755)
}

func (s *Store) Save(_ context.Context, in SaveInput) (Record, Snapshot, error) {
	if in.Snapshot.Version == 0 {
		in.Snapshot.Version = 1
	}
	if in.CWD != "" && in.Snapshot.CWD == "" {
		in.Snapshot.CWD = in.CWD
	}
	if in.Model != "" && in.Snapshot.Model == "" {
		in.Snapshot.Model = in.Model
	}
	if strings.TrimSpace(in.SessionID) == "" {
		in.SessionID = uuid.NewString()
	}
	now := time.Now().UnixNano()
	record := Record{
		ID:          in.SessionID,
		Title:       summarizePrompt(in.Prompt),
		Model:       firstNonEmpty(in.Snapshot.Model, in.Model),
		CWD:         filepath.Clean(firstNonEmpty(in.Snapshot.CWD, in.CWD)),
		Prompt:      strings.TrimSpace(in.Prompt),
		Termination: in.Termination,
		TurnCount:   len(in.Turns),
		UpdatedAt:   now,
	}
	doc, err := s.readDocument(in.SessionID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Record{}, Snapshot{}, err
	}
	if doc.Record.CreatedAt > 0 {
		record.CreatedAt = doc.Record.CreatedAt
	} else {
		record.CreatedAt = now
	}
	payload := sessionDocument{
		Record:   record,
		Snapshot: in.Snapshot,
		Turns:    cloneTurns(in.Turns),
	}
	if err := s.writeDocument(payload); err != nil {
		return Record{}, Snapshot{}, err
	}
	return record, in.Snapshot, nil
}

func (s *Store) Load(_ context.Context, selector string) (Record, Snapshot, []TurnRecord, error) {
	doc, err := s.loadDocument(selector)
	if err != nil {
		return Record{}, Snapshot{}, nil, err
	}
	return doc.Record, doc.Snapshot, cloneTurns(doc.Turns), nil
}

func (s *Store) List(_ context.Context, limit int) ([]Record, error) {
	docs, err := s.listDocuments()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(docs, func(i, j int) bool {
		return docs[i].Record.UpdatedAt > docs[j].Record.UpdatedAt
	})
	if limit > 0 && len(docs) > limit {
		docs = docs[:limit]
	}
	out := make([]Record, 0, len(docs))
	for _, doc := range docs {
		out = append(out, doc.Record)
	}
	return out, nil
}

func (s *Store) loadDocument(selector string) (sessionDocument, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || strings.EqualFold(selector, "latest") {
		docs, err := s.listDocuments()
		if err != nil {
			return sessionDocument{}, err
		}
		if len(docs) == 0 {
			return sessionDocument{}, fmt.Errorf("session not found")
		}
		sort.SliceStable(docs, func(i, j int) bool {
			return docs[i].Record.UpdatedAt > docs[j].Record.UpdatedAt
		})
		return docs[0], nil
	}
	return s.readDocument(selector)
}

func (s *Store) listDocuments() ([]sessionDocument, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	out := make([]sessionDocument, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		doc, err := s.readDocument(entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

func (s *Store) readDocument(sessionID string) (sessionDocument, error) {
	data, err := os.ReadFile(s.sessionFile(sessionID))
	if err != nil {
		return sessionDocument{}, err
	}
	var doc sessionDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return sessionDocument{}, fmt.Errorf("decode session document: %w", err)
	}
	return doc, nil
}

func (s *Store) writeDocument(doc sessionDocument) error {
	path := s.sessionFile(doc.Record.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session document: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) sessionFile(sessionID string) string {
	return filepath.Join(s.root, sanitizeSessionID(sessionID), "session.json")
}

func sessionRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return filepath.Join(".", "sessions")
	}
	clean := filepath.Clean(path)
	if strings.EqualFold(filepath.Base(clean), "sessions") {
		return clean
	}
	if filepath.Ext(clean) != "" {
		return filepath.Join(filepath.Dir(clean), "sessions")
	}
	return filepath.Join(clean, "sessions")
}

func sanitizeSessionID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "ad-hoc"
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(strings.TrimSpace(b.String()), "_")
	if out == "" {
		return "ad-hoc"
	}
	return out
}

func cloneTurns(in []TurnRecord) []TurnRecord {
	out := make([]TurnRecord, 0, len(in))
	for _, turn := range in {
		copied := turn
		if len(turn.SavedBy) > 0 {
			copied.SavedBy = make(map[string]int, len(turn.SavedBy))
			for k, v := range turn.SavedBy {
				copied.SavedBy[k] = v
			}
		}
		out = append(out, copied)
	}
	return out
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
