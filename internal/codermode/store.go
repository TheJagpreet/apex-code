package codermode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/domain"
)

type Store struct {
	root string
}

func OpenStore(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) WorkflowPath(wf domain.CoderWorkflow) string {
	return s.pathForWorkflow(wf)
}

func (s *Store) Save(_ context.Context, wf domain.CoderWorkflow) error {
	if err := ValidateWorkflow(wf); err != nil {
		return err
	}
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workflow: %w", err)
	}
	path := s.pathForWorkflow(wf)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) Load(_ context.Context, id string) (domain.CoderWorkflow, error) {
	path, err := s.findPath(id)
	if err != nil {
		return domain.CoderWorkflow{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.CoderWorkflow{}, err
	}
	var wf domain.CoderWorkflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return domain.CoderWorkflow{}, fmt.Errorf("decode workflow: %w", err)
	}
	return wf, ValidateWorkflow(wf)
}

func (s *Store) List(_ context.Context) ([]domain.CoderWorkflow, error) {
	var out []domain.CoderWorkflow
	err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "workflows" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var wf domain.CoderWorkflow
		if err := json.Unmarshal(data, &wf); err != nil {
			return err
		}
		if err := ValidateWorkflow(wf); err != nil {
			return err
		}
		out = append(out, wf)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *Store) LatestBySession(ctx context.Context, sessionID string) (domain.CoderWorkflow, bool, error) {
	workflows, err := s.List(ctx)
	if err != nil {
		return domain.CoderWorkflow{}, false, err
	}
	for _, wf := range workflows {
		if wf.SessionID == sessionID {
			return wf, true, nil
		}
	}
	return domain.CoderWorkflow{}, false, nil
}

func (s *Store) pathForWorkflow(wf domain.CoderWorkflow) string {
	timestampPart := wf.CreatedAt.UTC().Format("20060102T150405.000Z")
	sessionPart := sanitizeFilePart(wf.SessionID)
	if sessionPart == "" {
		sessionPart = "ad-hoc"
	}
	return filepath.Join(s.root, sessionPart, "workflows", timestampPart+"-"+sessionPart+"-"+sanitizeFilePart(wf.ID)+".json")
}

func (s *Store) findPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("workflow id is required")
	}
	direct := filepath.Join(s.root, id+".json")
	if _, err := os.Stat(direct); err == nil {
		return direct, nil
	}
	var found string
	suffix := "-" + sanitizeFilePart(id) + ".json"
	err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), suffix) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return "", err
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}

func ValidateWorkflow(wf domain.CoderWorkflow) error {
	if strings.TrimSpace(wf.ID) == "" {
		return fmt.Errorf("workflow id is required")
	}
	if wf.SchemaVersion == 0 {
		return fmt.Errorf("workflow schema_version is required")
	}
	if strings.TrimSpace(wf.Mode) == "" {
		return fmt.Errorf("workflow mode is required")
	}
	if wf.CreatedAt.IsZero() || wf.UpdatedAt.IsZero() {
		return fmt.Errorf("workflow timestamps are required")
	}
	if wf.PlanVersion < 1 {
		return fmt.Errorf("workflow plan_version must be >= 1")
	}
	taskIDs := map[string]bool{}
	for _, task := range wf.Tasks {
		if strings.TrimSpace(task.ID) == "" {
			return fmt.Errorf("workflow task id is required")
		}
		if taskIDs[task.ID] {
			return fmt.Errorf("duplicate task id %q", task.ID)
		}
		taskIDs[task.ID] = true
	}
	for _, task := range wf.Tasks {
		for _, dep := range task.Dependencies {
			if dep == "" || !taskIDs[dep] {
				return fmt.Errorf("task %q depends on unknown task %q", task.ID, dep)
			}
		}
	}
	return nil
}

func NewWorkflow(sessionID, prompt string) domain.CoderWorkflow {
	now := time.Now().UTC()
	return domain.CoderWorkflow{
		SchemaVersion: 1,
		ID:            workflowID(),
		SessionID:     strings.TrimSpace(sessionID),
		Mode:          "coder",
		UserPrompt:    strings.TrimSpace(prompt),
		Stages: domain.WorkflowStages{
			Orchestrator: domain.WorkflowStage{
				Status:    "pending",
				Input:     strings.TrimSpace(prompt),
				UpdatedAt: now,
			},
			Planner: domain.WorkflowStage{
				Status:    "pending",
				UpdatedAt: now,
			},
		},
		PlanVersion: 1,
		State:       domain.WorkflowStateDraft,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func sanitizeFilePart(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
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
	return strings.Trim(strings.TrimSpace(b.String()), "_")
}
