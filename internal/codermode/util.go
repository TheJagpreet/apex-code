package codermode

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/domain"
	"github.com/google/uuid"
)

func workflowID() string {
	return uuid.NewString()
}

func runID() string {
	return uuid.NewString()
}

func mutationID() string {
	return uuid.NewString()
}

func cloneWorkflow(wf domain.CoderWorkflow) domain.CoderWorkflow {
	data, _ := json.Marshal(wf)
	var out domain.CoderWorkflow
	_ = json.Unmarshal(data, &out)
	return out
}

func markReadyTasks(wf *domain.CoderWorkflow) {
	for i := range wf.Tasks {
		task := &wf.Tasks[i]
		if task.Status != domain.WorkflowTaskPending && task.Status != domain.WorkflowTaskNeedsPlan {
			continue
		}
		ready := true
		for _, dep := range task.Dependencies {
			if findTask(*wf, dep).Status != domain.WorkflowTaskDone {
				ready = false
				break
			}
		}
		if ready {
			task.Status = domain.WorkflowTaskReady
		}
	}
}

func findTask(wf domain.CoderWorkflow, id string) domain.WorkflowTask {
	for _, task := range wf.Tasks {
		if task.ID == id {
			return task
		}
	}
	return domain.WorkflowTask{}
}

func findTaskIndex(wf domain.CoderWorkflow, id string) int {
	for i, task := range wf.Tasks {
		if task.ID == id {
			return i
		}
	}
	return -1
}

func flattenPlan(plan domain.PlannerPlan) []domain.WorkflowTask {
	if len(plan.Tasks) > 0 {
		return plan.Tasks
	}
	var tasks []domain.WorkflowTask
	for _, phase := range plan.Phases {
		for _, id := range phase.TaskIDs {
			tasks = append(tasks, domain.WorkflowTask{
				ID:         id,
				Phase:      phase.Name,
				Title:      strings.ReplaceAll(strings.Title(strings.ReplaceAll(id, "_", " ")), " ", " "),
				Status:     domain.WorkflowTaskPending,
				OwnerAgent: domain.WorkflowAgentSolutioner,
			})
		}
	}
	return tasks
}

func summarizePlan(wf domain.CoderWorkflow) string {
	var phases []string
	for _, task := range wf.Tasks {
		if task.Phase != "" && !contains(phases, task.Phase) {
			phases = append(phases, task.Phase)
		}
	}
	return fmt.Sprintf("workflow=%s  state=%s  tasks=%d  phases=%d  version=%d", wf.ID, wf.State, len(wf.Tasks), len(phases), wf.PlanVersion)
}

func WorkflowBudgetText(wf domain.CoderWorkflow) string {
	var b strings.Builder
	b.WriteString("Coder workflow budget context\n")
	b.WriteString("user_prompt: ")
	b.WriteString(strings.TrimSpace(wf.UserPrompt))
	b.WriteString("\n")
	if strings.TrimSpace(wf.EnrichedPrompt) != "" {
		b.WriteString("enriched_prompt: ")
		b.WriteString(strings.TrimSpace(wf.EnrichedPrompt))
		b.WriteString("\n")
	}
	b.WriteString("state: ")
	b.WriteString(string(wf.State))
	b.WriteString("\n")
	for _, task := range wf.Tasks {
		b.WriteString("- task ")
		b.WriteString(task.ID)
		b.WriteString(" ")
		b.WriteString(string(task.OwnerAgent))
		b.WriteString(" ")
		b.WriteString(string(task.Status))
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(firstNonEmptyTaskText(task.Description, task.Title)))
		b.WriteString("\n")
		for _, output := range tailStrings(task.Outputs, 1) {
			b.WriteString("  output: ")
			b.WriteString(strings.TrimSpace(output))
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(wf.ExecutionSummary) != "" {
		b.WriteString("execution_summary: ")
		b.WriteString(strings.TrimSpace(wf.ExecutionSummary))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func firstNonEmptyTaskText(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func buildExecutionSummary(wf domain.CoderWorkflow) string {
	var b strings.Builder
	b.WriteString("Workflow execution sequence:\n")
	for i, run := range wf.RunHistory {
		if strings.TrimSpace(run.Reason) == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("%d. %s", i+1, run.Agent))
		if strings.TrimSpace(run.TaskID) != "" {
			b.WriteString(" (" + run.TaskID + ")")
		}
		b.WriteString(" -> ")
		if strings.TrimSpace(run.Error) != "" {
			b.WriteString("failed: " + strings.TrimSpace(run.Error))
		} else if summary := structuredSummary(run); summary != "" {
			b.WriteString(summary)
		} else {
			b.WriteString(strings.TrimSpace(run.Reason))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func structuredSummary(run domain.WorkflowAgentRun) string {
	if run.Structured != nil {
		if summary, ok := run.Structured["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			return strings.TrimSpace(summary)
		}
	}
	if strings.TrimSpace(run.Output) != "" {
		return compactSummary(run.Output)
	}
	return ""
}

func compactSummary(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = strings.TrimSpace(s[:idx]) + " …"
	}
	if len(s) > 160 {
		s = s[:160] + " …"
	}
	return s
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func appendRun(wf *domain.CoderWorkflow, run domain.WorkflowAgentRun) {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if run.CompletedAt.IsZero() {
		run.CompletedAt = run.StartedAt
	}
	wf.RunHistory = append(wf.RunHistory, run)
	if run.Agent != "" {
		wf.ActiveAgent = run.Agent
		if !agentSeen(wf.CompletedAgents, run.Agent) {
			wf.CompletedAgents = append(wf.CompletedAgents, run.Agent)
		}
	}
	wf.UpdatedAt = time.Now().UTC()
}

func agentSeen(items []domain.WorkflowAgent, want domain.WorkflowAgent) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
