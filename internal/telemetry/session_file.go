package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/apex-code/apex/internal/domain"
)

type SessionEvent struct {
	Index             int                 `json:"index"`
	Timestamp         time.Time           `json:"timestamp"`
	Mode              string              `json:"mode,omitempty"`
	Kind              string              `json:"kind"`
	Outcome           string              `json:"outcome,omitempty"`
	Recoverable       bool                `json:"recoverable,omitempty"`
	Model             string              `json:"model,omitempty"`
	PromptTokens      int                 `json:"prompt_tokens,omitempty"`
	CompletionTokens  int                 `json:"completion_tokens,omitempty"`
	TotalTokens       int                 `json:"total_tokens,omitempty"`
	CacheCreation     int                 `json:"cache_creation_tokens,omitempty"`
	CacheRead         int                 `json:"cache_read_tokens,omitempty"`
	DurationMs        int64               `json:"duration_ms,omitempty"`
	Termination       string              `json:"termination,omitempty"`
	WorkflowID        string              `json:"workflow_id,omitempty"`
	TaskID            string              `json:"task_id,omitempty"`
	Agent             string              `json:"agent,omitempty"`
	CustomAgent       string              `json:"custom_agent,omitempty"`
	CustomAgentFile   string              `json:"custom_agent_file,omitempty"`
	CustomSkills      []string            `json:"custom_skills,omitempty"`
	CustomSkillFiles  []string            `json:"custom_skill_files,omitempty"`
	ToolCalls         []string            `json:"tool_calls,omitempty"`
	ToolCallDetails   []domain.ToolCall   `json:"tool_call_details,omitempty"`
	ToolResults       int                 `json:"tool_results,omitempty"`
	ToolResultDetails []domain.ToolResult `json:"tool_result_details,omitempty"`
	InputMessages     []domain.Message    `json:"input_messages,omitempty"`
	OutputMessage     *domain.Message     `json:"output_message,omitempty"`
	SavedBy           map[string]int      `json:"saved_by,omitempty"`
	Error             string              `json:"error,omitempty"`
}

type SessionArtifact struct {
	SchemaVersion int            `json:"schema_version"`
	SessionID     string         `json:"session_id"`
	Model         string         `json:"model,omitempty"`
	CWD           string         `json:"cwd,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Events        []SessionEvent `json:"events"`
}

type FileMeta struct {
	Model string
	CWD   string
}

type FileStore struct {
	root string
	mu   sync.Mutex
}

func OpenFileStore(root string) (*FileStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) Root() string { return s.root }

func (s *FileStore) SessionDir(sessionID string) string {
	return filepath.Join(s.root, sanitizeSessionPart(sessionID))
}

func (s *FileStore) WorkflowDir(sessionID string) string {
	return filepath.Join(s.SessionDir(sessionID), "workflows")
}

func (s *FileStore) TelemetryPath(sessionID string) string {
	return filepath.Join(s.SessionDir(sessionID), "telemetry.json")
}

func (s *FileStore) AppendEvent(_ context.Context, sessionID string, meta FileMeta, event SessionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	path := s.TelemetryPath(sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	doc, err := s.readLocked(path, sessionID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.Model) != "" {
		doc.Model = strings.TrimSpace(meta.Model)
	}
	if strings.TrimSpace(meta.CWD) != "" {
		doc.CWD = strings.TrimSpace(meta.CWD)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	doc.Events = append(doc.Events, cloneSessionEvent(event))
	doc.UpdatedAt = event.Timestamp
	return s.writeLocked(path, doc)
}

func (s *FileStore) SessionTotals(_ context.Context, sessionID string) (Totals, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readLocked(s.TelemetryPath(sessionID), sessionID)
	if err != nil {
		if os.IsNotExist(err) {
			return Totals{SavedBy: map[string]int{}}, 0, nil
		}
		return Totals{}, 0, err
	}
	total := Totals{SavedBy: map[string]int{}}
	for _, event := range doc.Events {
		total.Turns++
		total.PromptTokens += event.PromptTokens
		total.CompletionTokens += event.CompletionTokens
		total.TotalTokens += event.TotalTokens
		total.CacheCreation += event.CacheCreation
		total.CacheRead += event.CacheRead
		total.DurationMs += event.DurationMs
		if event.Timestamp.Unix() > 0 && (total.FirstAt == 0 || event.Timestamp.Unix() < total.FirstAt) {
			total.FirstAt = event.Timestamp.Unix()
		}
		if event.Timestamp.Unix() > total.LastAt {
			total.LastAt = event.Timestamp.Unix()
		}
		if event.Model != "" && !containsString(total.Models, event.Model) {
			total.Models = append(total.Models, event.Model)
		}
		for name, value := range event.SavedBy {
			total.SavedBy[name] += value
		}
	}
	return total, len(doc.Events), nil
}

func (s *FileStore) readLocked(path, sessionID string) (SessionArtifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			now := time.Now().UTC()
			return SessionArtifact{
				SchemaVersion: 1,
				SessionID:     strings.TrimSpace(sessionID),
				CreatedAt:     now,
				UpdatedAt:     now,
				Events:        []SessionEvent{},
			}, nil
		}
		return SessionArtifact{}, err
	}
	var doc SessionArtifact
	if err := json.Unmarshal(data, &doc); err != nil {
		return SessionArtifact{}, fmt.Errorf("decode telemetry artifact: %w", err)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = 1
	}
	if strings.TrimSpace(doc.SessionID) == "" {
		doc.SessionID = strings.TrimSpace(sessionID)
	}
	return doc, nil
}

func (s *FileStore) writeLocked(path string, doc SessionArtifact) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode telemetry artifact: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sanitizeSessionPart(v string) string {
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

func cloneSessionEvent(in SessionEvent) SessionEvent {
	out := in
	out.CustomSkills = append([]string(nil), in.CustomSkills...)
	out.CustomSkillFiles = append([]string(nil), in.CustomSkillFiles...)
	out.ToolCalls = append([]string(nil), in.ToolCalls...)
	if len(in.ToolCallDetails) > 0 {
		out.ToolCallDetails = make([]domain.ToolCall, 0, len(in.ToolCallDetails))
		for _, call := range in.ToolCallDetails {
			out.ToolCallDetails = append(out.ToolCallDetails, domain.ToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: append(json.RawMessage(nil), call.Arguments...),
			})
		}
	}
	if len(in.ToolResultDetails) > 0 {
		out.ToolResultDetails = make([]domain.ToolResult, 0, len(in.ToolResultDetails))
		for _, result := range in.ToolResultDetails {
			out.ToolResultDetails = append(out.ToolResultDetails, result)
		}
	}
	if len(in.InputMessages) > 0 {
		out.InputMessages = cloneMessages(in.InputMessages)
	}
	if in.OutputMessage != nil {
		copied := cloneMessages([]domain.Message{*in.OutputMessage})
		if len(copied) == 1 {
			out.OutputMessage = &copied[0]
		}
	}
	if len(in.SavedBy) > 0 {
		out.SavedBy = make(map[string]int, len(in.SavedBy))
		for k, v := range in.SavedBy {
			out.SavedBy[k] = v
		}
	}
	return out
}

func cloneMessages(messages []domain.Message) []domain.Message {
	out := make([]domain.Message, 0, len(messages))
	for _, msg := range messages {
		copied := domain.Message{
			Role:         msg.Role,
			Content:      msg.Content,
			CacheControl: msg.CacheControl,
		}
		if len(msg.ToolCalls) > 0 {
			copied.ToolCalls = make([]domain.ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				copied.ToolCalls = append(copied.ToolCalls, domain.ToolCall{
					ID:        call.ID,
					Name:      call.Name,
					Arguments: append(json.RawMessage(nil), call.Arguments...),
				})
			}
		}
		if len(msg.ToolResults) > 0 {
			copied.ToolResults = make([]domain.ToolResult, 0, len(msg.ToolResults))
			for _, result := range msg.ToolResults {
				copied.ToolResults = append(copied.ToolResults, result)
			}
		}
		out = append(out, copied)
	}
	return out
}
