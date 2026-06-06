package telemetry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/google/uuid"
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

func (t Totals) AvgLatencyMs() int64 {
	if t.Turns == 0 || t.DurationMs == 0 {
		return 0
	}
	return t.DurationMs / int64(t.Turns)
}

type Store struct {
	files *FileStore
}

func Open(path string) (*Store, error) {
	root, err := telemetrySessionRoot(path)
	if err != nil {
		return nil, err
	}
	files, err := OpenFileStore(root)
	if err != nil {
		return nil, err
	}
	return &Store{files: files}, nil
}

func OpenMemory() (*Store, error) {
	return Open(filepath.Join(os.TempDir(), "apex-telemetry-memory-"+uuid.NewString()))
}

func (s *Store) Close() error { return nil }

func (s *Store) Init(_ context.Context) error { return nil }

func (s *Store) SaveTurn(ctx context.Context, metric TurnMetric) error {
	if metric.CreatedAt == 0 {
		metric.CreatedAt = time.Now().Unix()
	}
	if s == nil || s.files == nil {
		return fmt.Errorf("telemetry store is not initialized")
	}
	return s.files.AppendEvent(ctx, metric.SessionID, FileMeta{Model: metric.Model}, SessionEvent{
		Index:            metric.TurnIndex,
		Timestamp:        time.Unix(metric.CreatedAt, 0).UTC(),
		Mode:             "chat",
		Kind:             "llm_turn",
		Model:            metric.Model,
		PromptTokens:     metric.PromptTokens,
		CompletionTokens: metric.CompletionTokens,
		TotalTokens:      metric.TotalTokens,
		CacheCreation:    metric.CacheCreation,
		CacheRead:        metric.CacheRead,
		DurationMs:       metric.DurationMs,
		Termination:      metric.Termination,
		SavedBy:          cloneSavedBy(metric.SavedBy),
	})
}

func (s *Store) queryMetrics(_ context.Context, sessionID string) ([]TurnMetric, error) {
	if s == nil || s.files == nil {
		return nil, nil
	}
	artifacts, err := s.readArtifacts(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]TurnMetric, 0)
	for _, doc := range artifacts {
		for _, event := range doc.Events {
			if !isLLMEventKind(event.Kind) {
				continue
			}
			out = append(out, TurnMetric{
				SessionID:        doc.SessionID,
				TurnIndex:        event.Index,
				Model:            event.Model,
				PromptTokens:     event.PromptTokens,
				CompletionTokens: event.CompletionTokens,
				TotalTokens:      event.TotalTokens,
				CacheCreation:    event.CacheCreation,
				CacheRead:        event.CacheRead,
				DurationMs:       event.DurationMs,
				Termination:      event.Termination,
				SavedBy:          cloneSavedBy(event.SavedBy),
				CreatedAt:        event.Timestamp.Unix(),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].TurnIndex < out[j].TurnIndex
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out, nil
}

func isLLMEventKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "llm_turn", "stage_llm", "task_llm_turn":
		return true
	default:
		return false
	}
}

func (s *Store) readArtifacts(sessionID string) ([]SessionArtifact, error) {
	if strings.TrimSpace(sessionID) != "" {
		doc, err := s.files.readLocked(s.files.TelemetryPath(sessionID), sessionID)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		return []SessionArtifact{doc}, nil
	}
	entries, err := os.ReadDir(s.files.Root())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]SessionArtifact, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		doc, err := s.files.readLocked(filepath.Join(s.files.Root(), entry.Name(), "telemetry.json"), entry.Name())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
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
	if m.Model != "" && !containsString(t.Models, m.Model) {
		t.Models = append(t.Models, m.Model)
	}
	for name, value := range m.SavedBy {
		t.SavedBy[name] += value
	}
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
	last map[string]int
}

func (c *Collector) Render(report contextmgr.RenderReport) {
	c.last = cloneSavedBy(report.SavedBy)
}

func (c *Collector) LastSavedBy() map[string]int {
	return cloneSavedBy(c.last)
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

func cloneSavedBy(in map[string]int) map[string]int {
	if len(in) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func telemetrySessionRoot(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return filepath.Join(".", "sessions"), nil
	}
	clean := filepath.Clean(path)
	if strings.EqualFold(filepath.Base(clean), "sessions") {
		return clean, nil
	}
	if filepath.Ext(clean) != "" {
		return filepath.Join(filepath.Dir(clean), "sessions"), nil
	}
	return filepath.Join(clean, "sessions"), nil
}
