package cli

import (
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/domain"
)

func TestRenderWorkflowExecutionSummaryOmitsDuplicatedTaskOutputs(t *testing.T) {
	wf := domain.CoderWorkflow{
		SessionID:   "session-1",
		ID:          "workflow-1",
		PlanVersion: 2,
		State:       domain.WorkflowStateCompleted,
		UserPrompt:  "review the readme",
		Tasks: []domain.WorkflowTask{
			{
				ID:         "t1",
				Title:      "Review README",
				OwnerAgent: domain.WorkflowAgentTester,
				Status:     domain.WorkflowTaskDone,
				Outputs: []string{
					"| Area | Current Issue | Suggestion |\n| --- | --- | --- |\n| README | Missing quick start | Add one |",
				},
			},
		},
		RunHistory: []domain.WorkflowAgentRun{
			{Agent: domain.WorkflowAgentPlanner},
			{Agent: domain.WorkflowAgentTester},
		},
	}

	out := renderWorkflowExecutionSummary(wf, "Coder workflow execution completed.")
	if strings.Contains(out, "## Task Outputs") {
		t.Fatalf("summary should omit duplicated task outputs: %q", out)
	}
	if strings.Contains(out, "| Area | Current Issue | Suggestion |") {
		t.Fatalf("summary should not repeat detailed task output: %q", out)
	}
}
