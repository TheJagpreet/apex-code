package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/contextmgr"
	_ "modernc.org/sqlite"
)

type TurnMetric struct {
	SessionID        string
	TurnIndex        int
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheCreation    int
	CacheRead        int
	DurationMs       int64
	Termination      string
	SavedBy          map[string]int
	CreatedAt        int64
}

type Totals struct {
	Turns            int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheCreation    int
	CacheRead        int
	DurationMs       int64
	FirstAt          int64
	LastAt           int64
	Models           []string
	SavedBy          map[string]int
}

// AvgLatencyMs returns the mean per-turn latency, or 0 when unknown.
func (t Totals) AvgLatencyMs() int64 {
	if t.Turns == 0 || t.DurationMs == 0 {
		return 0
	}
	return t.DurationMs / int64(t.Turns)
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
	_, err := s.db.ExecContext(ctx, `create table if not exists turn_metrics (
		session_id text not null,
		turn_index integer not null,
		model text not null,
		prompt_tokens integer not null,
		completion_tokens integer not null,
		total_tokens integer not null,
		cache_creation_tokens integer not null,
		cache_read_tokens integer not null,
		termination text not null,
		saved_by_json text not null,
		created_at integer not null,
		primary key(session_id, turn_index)
	)`)
	if err != nil {
		return err
	}
	// Best-effort migration for databases created before the latency dimension
	// existed. A duplicate-column error is expected and ignored on upgraded DBs.
	if _, err := s.db.ExecContext(ctx, `alter table turn_metrics add column duration_ms integer not null default 0`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	// Index the dimensions we trace by so model/session/time rollups stay cheap.
	for _, idx := range []string{
		`create index if not exists idx_turn_metrics_model on turn_metrics(model)`,
		`create index if not exists idx_turn_metrics_created on turn_metrics(created_at)`,
	} {
		if _, err := s.db.ExecContext(ctx, idx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveTurn(ctx context.Context, metric TurnMetric) error {
	if metric.CreatedAt == 0 {
		metric.CreatedAt = time.Now().Unix()
	}
	savedByJSON, err := json.Marshal(metric.SavedBy)
	if err != nil {
		return fmt.Errorf("marshal telemetry savings: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `insert into turn_metrics(
		session_id, turn_index, model, prompt_tokens, completion_tokens, total_tokens,
		cache_creation_tokens, cache_read_tokens, duration_ms, termination, saved_by_json, created_at
	) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	on conflict(session_id, turn_index) do update set
		model=excluded.model,
		prompt_tokens=excluded.prompt_tokens,
		completion_tokens=excluded.completion_tokens,
		total_tokens=excluded.total_tokens,
		cache_creation_tokens=excluded.cache_creation_tokens,
		cache_read_tokens=excluded.cache_read_tokens,
		duration_ms=excluded.duration_ms,
		termination=excluded.termination,
		saved_by_json=excluded.saved_by_json,
		created_at=excluded.created_at`,
		metric.SessionID, metric.TurnIndex, metric.Model, metric.PromptTokens, metric.CompletionTokens, metric.TotalTokens,
		metric.CacheCreation, metric.CacheRead, metric.DurationMs, metric.Termination, string(savedByJSON), metric.CreatedAt)
	return err
}

const metricColumns = `session_id, turn_index, model, prompt_tokens, completion_tokens, total_tokens,
	cache_creation_tokens, cache_read_tokens, duration_ms, termination, saved_by_json, created_at`

func scanMetric(rows *sql.Rows) (TurnMetric, error) {
	var m TurnMetric
	var savedByJSON string
	if err := rows.Scan(&m.SessionID, &m.TurnIndex, &m.Model, &m.PromptTokens, &m.CompletionTokens,
		&m.TotalTokens, &m.CacheCreation, &m.CacheRead, &m.DurationMs, &m.Termination, &savedByJSON, &m.CreatedAt); err != nil {
		return TurnMetric{}, err
	}
	m.SavedBy = map[string]int{}
	_ = json.Unmarshal([]byte(savedByJSON), &m.SavedBy)
	return m, nil
}

// queryMetrics returns rows for the whole store (empty sessionID) or one session.
func (s *Store) queryMetrics(ctx context.Context, sessionID string) ([]TurnMetric, error) {
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(sessionID) == "" {
		rows, err = s.db.QueryContext(ctx, `select `+metricColumns+` from turn_metrics order by created_at, turn_index`)
	} else {
		rows, err = s.db.QueryContext(ctx, `select `+metricColumns+` from turn_metrics where session_id = ? order by turn_index`, sessionID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TurnMetric
	for rows.Next() {
		m, err := scanMetric(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// accumulate folds one metric into a running Totals, tracking the token, latency,
// time-window, and model dimensions.
func (t *Totals) accumulate(m TurnMetric) {
	if t.SavedBy == nil {
		t.SavedBy = map[string]int{}
	}
	t.Turns++
	t.PromptTokens += m.PromptTokens
	t.CompletionTokens += m.CompletionTokens
	t.TotalTokens += m.TotalTokens
	t.CacheCreation += m.CacheCreation
	t.CacheRead += m.CacheRead
	t.DurationMs += m.DurationMs
	if m.CreatedAt > 0 && (t.FirstAt == 0 || m.CreatedAt < t.FirstAt) {
		t.FirstAt = m.CreatedAt
	}
	if m.CreatedAt > t.LastAt {
		t.LastAt = m.CreatedAt
	}
	if m.Model != "" {
		if !containsString(t.Models, m.Model) {
			t.Models = append(t.Models, m.Model)
		}
	}
	for name, value := range m.SavedBy {
		t.SavedBy[name] += value
	}
}

func (s *Store) Totals(ctx context.Context, sessionID string) (Totals, error) {
	metrics, err := s.queryMetrics(ctx, sessionID)
	if err != nil {
		return Totals{}, err
	}
	total := Totals{SavedBy: map[string]int{}}
	for _, m := range metrics {
		total.accumulate(m)
	}
	sort.Strings(total.Models)
	return total, nil
}

// ByModel rolls usage up per model — the model-based trace dimension. Pass an
// empty sessionID to roll up across every session.
func (s *Store) ByModel(ctx context.Context, sessionID string) (map[string]Totals, error) {
	metrics, err := s.queryMetrics(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := map[string]Totals{}
	for _, m := range metrics {
		t := out[m.Model]
		t.accumulate(m)
		out[m.Model] = t
	}
	return out, nil
}

// BySession rolls usage up per session id — the session-id map dimension.
func (s *Store) BySession(ctx context.Context) (map[string]Totals, error) {
	metrics, err := s.queryMetrics(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string]Totals{}
	for _, m := range metrics {
		t := out[m.SessionID]
		t.accumulate(m)
		out[m.SessionID] = t
	}
	return out, nil
}

// Recent returns the most recent turn-level traces (newest first), bounded by
// limit. Pass an empty sessionID to span every session.
func (s *Store) Recent(ctx context.Context, sessionID string, limit int) ([]TurnMetric, error) {
	metrics, err := s.queryMetrics(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(metrics, func(i, j int) bool { return metrics[i].CreatedAt > metrics[j].CreatedAt })
	if limit > 0 && len(metrics) > limit {
		metrics = metrics[:limit]
	}
	return metrics, nil
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

type Collector struct {
	last contextmgr.RenderReport
}

func (c *Collector) Render(report contextmgr.RenderReport) {
	c.last = report
}

func (c *Collector) LastSavedBy() map[string]int {
	if len(c.last.SavedBy) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(c.last.SavedBy))
	for k, v := range c.last.SavedBy {
		out[k] = v
	}
	return out
}

func FormatTotals(t Totals) string {
	var parts []string
	parts = append(parts,
		fmt.Sprintf("turns=%d", t.Turns),
		fmt.Sprintf("prompt=%d", t.PromptTokens),
		fmt.Sprintf("completion=%d", t.CompletionTokens),
		fmt.Sprintf("total=%d", t.TotalTokens),
	)
	if cacheRatio := CacheHitRatio(t); cacheRatio > 0 {
		parts = append(parts, fmt.Sprintf("cache-hit=%.1f%%", cacheRatio*100))
	}
	if avg := t.AvgLatencyMs(); avg > 0 {
		parts = append(parts, fmt.Sprintf("avg-latency=%dms", avg))
	}
	if len(t.Models) > 0 {
		parts = append(parts, "models["+strings.Join(t.Models, ",")+"]")
	}
	if t.LastAt > 0 {
		parts = append(parts, "last="+time.Unix(t.LastAt, 0).Format(time.RFC3339))
	}
	if savings := formatSavedBy(t.SavedBy); savings != "" {
		parts = append(parts, "saved["+savings+"]")
	}
	return strings.Join(parts, "  ")
}

// FormatByModel renders a per-model usage breakdown, one line per model, sorted
// by name for stable output.
func FormatByModel(byModel map[string]Totals) string {
	if len(byModel) == 0 {
		return "(no model traces yet)"
	}
	names := make([]string, 0, len(byModel))
	for name := range byModel {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		t := byModel[name]
		label := name
		if label == "" {
			label = "(unknown)"
		}
		line := fmt.Sprintf("%s  turns=%d  total=%d", label, t.Turns, t.TotalTokens)
		if avg := t.AvgLatencyMs(); avg > 0 {
			line += fmt.Sprintf("  avg-latency=%dms", avg)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// FormatTrace renders recent turn-level traces as timestamped lines across the
// session, model, consumption, and latency dimensions.
func FormatTrace(metrics []TurnMetric) string {
	if len(metrics) == 0 {
		return "(no traces yet)"
	}
	lines := make([]string, 0, len(metrics))
	for _, m := range metrics {
		ts := "-"
		if m.CreatedAt > 0 {
			ts = time.Unix(m.CreatedAt, 0).Format(time.RFC3339)
		}
		line := fmt.Sprintf("%s  session=%s  turn=%d  model=%s  total=%d", ts, m.SessionID, m.TurnIndex, m.Model, m.TotalTokens)
		if m.DurationMs > 0 {
			line += fmt.Sprintf("  latency=%dms", m.DurationMs)
		}
		if m.Termination != "" {
			line += "  term=" + m.Termination
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func CacheHitRatio(t Totals) float64 {
	denom := t.CacheCreation + t.CacheRead
	if denom == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(denom)
}

func formatSavedBy(savedBy map[string]int) string {
	if len(savedBy) == 0 {
		return ""
	}
	names := make([]string, 0, len(savedBy))
	for name := range savedBy {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, savedBy[name]))
	}
	return strings.Join(parts, ", ")
}
