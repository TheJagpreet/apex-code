package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type WorkflowState string

const (
	WorkflowStateDraft      WorkflowState = "draft"
	WorkflowStatePlanReady  WorkflowState = "plan_ready"
	WorkflowStatePlanReview WorkflowState = "plan_review"
	WorkflowStateApproved   WorkflowState = "approved"
	WorkflowStateExecuting  WorkflowState = "executing"
	WorkflowStateReplanning WorkflowState = "replanning"
	WorkflowStatePaused     WorkflowState = "paused"
	WorkflowStateCompleted  WorkflowState = "completed"
	WorkflowStateFailed     WorkflowState = "failed"
)

type WorkflowTaskStatus string

const (
	WorkflowTaskPending   WorkflowTaskStatus = "pending"
	WorkflowTaskReady     WorkflowTaskStatus = "ready"
	WorkflowTaskRunning   WorkflowTaskStatus = "running"
	WorkflowTaskDone      WorkflowTaskStatus = "done"
	WorkflowTaskBlocked   WorkflowTaskStatus = "blocked"
	WorkflowTaskSkipped   WorkflowTaskStatus = "skipped"
	WorkflowTaskNeedsPlan WorkflowTaskStatus = "needs_replan"
	WorkflowTaskCancelled WorkflowTaskStatus = "cancelled"
)

type WorkflowAgent string

const (
	WorkflowAgentPlanner      WorkflowAgent = "planner"
	WorkflowAgentArchitecture WorkflowAgent = "architecture"
	WorkflowAgentSolutioner   WorkflowAgent = "solutioner"
	WorkflowAgentTester       WorkflowAgent = "tester"
	WorkflowAgentReviewer     WorkflowAgent = "reviewer"
)

type WorkflowMutationType string

const (
	WorkflowMutationInsert  WorkflowMutationType = "insert_task"
	WorkflowMutationUpdate  WorkflowMutationType = "update_task"
	WorkflowMutationDelete  WorkflowMutationType = "delete_task"
	WorkflowMutationReorder WorkflowMutationType = "reorder_task"
	WorkflowMutationStatus  WorkflowMutationType = "status_reset"
)

type CoderWorkflow struct {
	SchemaVersion       int                `json:"schema_version"`
	ID                  string             `json:"id"`
	SessionID           string             `json:"session_id,omitempty"`
	Mode                string             `json:"mode"`
	UserPrompt          string             `json:"user_prompt"`
	Stages              WorkflowStages     `json:"stages"`
	ExecutionSummary    string             `json:"execution_summary,omitempty"`
	PlanVersion         int                `json:"plan_version"`
	State               WorkflowState      `json:"state"`
	ActiveAgent         WorkflowAgent      `json:"active_agent,omitempty"`
	ActiveTaskID        string             `json:"active_task_id,omitempty"`
	CompletedAgents     []WorkflowAgent    `json:"completed_agents,omitempty"`
	Tasks               []WorkflowTask     `json:"tasks"`
	RunHistory          []WorkflowAgentRun `json:"run_history,omitempty"`
	Mutations           []WorkflowMutation `json:"mutations,omitempty"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

type WorkflowStages struct {
	Planner      WorkflowStage `json:"planner"`
}

type WorkflowStage struct {
	Status    string    `json:"status,omitempty"`
	Input     string    `json:"input,omitempty"`
	Output    string    `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type WorkflowTask struct {
	ID                 string             `json:"id"`
	Phase              string             `json:"phase"`
	Title              string             `json:"title"`
	Description        string             `json:"description"`
	Dependencies       []string           `json:"dependencies,omitempty"`
	Status             WorkflowTaskStatus `json:"status"`
	OwnerAgent         WorkflowAgent      `json:"owner_agent"`
	AcceptanceCriteria []string           `json:"acceptance_criteria,omitempty"`
	Outputs            []string           `json:"outputs,omitempty"`
}

func (t *WorkflowTask) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.ID = decodeFlexibleString(raw["id"])
	t.Phase = decodeFlexibleString(raw["phase"])
	t.Title = decodeFlexibleString(raw["title"])
	t.Description = decodeFlexibleString(raw["description"])
	t.Dependencies = decodeFlexibleStringSlice(raw["dependencies"])
	t.Status = WorkflowTaskStatus(decodeFlexibleString(raw["status"]))
	t.OwnerAgent = WorkflowAgent(decodeFlexibleString(raw["owner_agent"]))
	t.AcceptanceCriteria = decodeFlexibleStringSlice(raw["acceptance_criteria"])
	t.Outputs = decodeFlexibleStringSlice(raw["outputs"])
	return nil
}

type WorkflowAgentRun struct {
	ID              string             `json:"id"`
	Agent           WorkflowAgent      `json:"agent"`
	Reason          string             `json:"reason"`
	TaskID          string             `json:"task_id,omitempty"`
	Input           string             `json:"input,omitempty"`
	Output          string             `json:"output,omitempty"`
	Structured      map[string]any     `json:"structured,omitempty"`
	RequestedReplan bool               `json:"requested_replan,omitempty"`
	Mutations       []WorkflowMutation `json:"mutations,omitempty"`
	Error           string             `json:"error,omitempty"`
	PromptTokens    int                `json:"prompt_tokens,omitempty"`
	CompletionTokens int               `json:"completion_tokens,omitempty"`
	TotalTokens     int                `json:"total_tokens,omitempty"`
	BudgetPromptTokens int             `json:"budget_prompt_tokens,omitempty"`
	BudgetPromptLimit int              `json:"budget_prompt_limit,omitempty"`
	BudgetOutputHeadroom int           `json:"budget_output_headroom,omitempty"`
	DurationMs      int64              `json:"duration_ms,omitempty"`
	StartedAt       time.Time          `json:"started_at"`
	CompletedAt     time.Time          `json:"completed_at"`
}

type WorkflowMutation struct {
	ID          string               `json:"id"`
	Type        WorkflowMutationType `json:"type"`
	TaskID      string               `json:"task_id,omitempty"`
	Description string               `json:"description"`
	Before      map[string]any       `json:"before,omitempty"`
	After       map[string]any       `json:"after,omitempty"`
	Agent       WorkflowAgent        `json:"agent"`
	CreatedAt   time.Time            `json:"created_at"`
}

type PlannerPlan struct {
	Summary string         `json:"summary"`
	Phases  []PlannerPhase `json:"phases"`
	Tasks   []WorkflowTask `json:"tasks"`
}

type PlannerPhase struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	TaskIDs     []string `json:"task_ids"`
}

func (p *PlannerPhase) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Name = decodeFlexibleString(raw["name"])
	p.Description = decodeFlexibleString(raw["description"])
	p.TaskIDs = decodeFlexibleStringSlice(raw["task_ids"])
	return nil
}

func decodeFlexibleString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var anyValue any
	if err := json.Unmarshal(raw, &anyValue); err != nil {
		return ""
	}
	return normalizeAnyString(anyValue)
}

func decodeFlexibleStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var anyValue any
	if err := json.Unmarshal(raw, &anyValue); err != nil {
		return nil
	}
	switch values := anyValue.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if normalized := normalizeAnyString(value); normalized != "" {
				out = append(out, normalized)
			}
		}
		return out
	default:
		if normalized := normalizeAnyString(values); normalized != "" {
			return []string{normalized}
		}
		return nil
	}
}

func normalizeAnyString(v any) string {
	switch item := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(item)
	case float64:
		return fmt.Sprintf("%.0f", item)
	case bool:
		return fmt.Sprintf("%t", item)
	default:
		return strings.TrimSpace(fmt.Sprint(item))
	}
}
