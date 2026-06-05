package codermode_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/codermode"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/fake"
)

func TestWorkflowStoreRoundTrip(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	wf := codermode.NewWorkflow("session-1", "build coder mode")
	wf.Tasks = []domain.WorkflowTask{
		{ID: "t1", Title: "Plan", Status: domain.WorkflowTaskReady, OwnerAgent: domain.WorkflowAgentPlanner},
	}
	if err := store.Save(context.Background(), wf); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load(context.Background(), wf.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ID != wf.ID || got.Tasks[0].ID != "t1" {
		t.Fatalf("workflow round-trip mismatch: %+v", got)
	}
	if got.Stages.Orchestrator.Input != "build coder mode" {
		t.Fatalf("expected initial orchestrator input seeded in workflow json, got %+v", got.Stages)
	}
	if _, err := filepath.Abs(store.Root()); err != nil {
		t.Fatalf("root path should be usable: %v", err)
	}
	entries, err := os.ReadDir(store.Root())
	if err != nil {
		t.Fatalf("read workflow dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "session-1" || !entries[0].IsDir() {
		t.Fatalf("workflow root should contain session directory, got %v", entries)
	}
	workflowEntries, err := os.ReadDir(filepath.Join(store.Root(), "session-1", "workflows"))
	if err != nil {
		t.Fatalf("read workflow session dir: %v", err)
	}
	if len(workflowEntries) != 1 || !strings.Contains(workflowEntries[0].Name(), "-session-1-") {
		t.Fatalf("workflow filename should include session id, got %v", workflowEntries)
	}
}

func TestEngineCreateApproveExecuteWorkflow(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Implement coder mode","planner_instructions":"Prefer repo-specific steps"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"planning","description":"plan it","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"planning","title":"Inspect architecture","description":"Inspect the architecture and summarize it","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Identify relevant packages"],"outputs":[]} ]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"status":"done","summary":"Inspected the architecture and documented the main packages."}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"The orchestrator enriched the request, the planner created one architecture task, the architecture agent inspected the repo, and the workflow completed successfully."}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-1", "Build coder mode")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if wf.State != domain.WorkflowStatePlanReview || len(wf.Tasks) != 1 {
		t.Fatalf("unexpected workflow after planning: %+v", wf)
	}
	if wf.Stages.Orchestrator.Output == "" || wf.Stages.Planner.Output == "" {
		t.Fatalf("expected raw stage outputs to be stored in workflow: %+v", wf.Stages)
	}
	if wf.Stages.Planner.Input == "" {
		t.Fatalf("expected planner input to be stored in workflow: %+v", wf.Stages)
	}
	if !strings.Contains(wf.EnrichedPrompt, "coder mode") {
		t.Fatalf("enriched prompt missing expected content: %q", wf.EnrichedPrompt)
	}
	wf, err = engine.ApprovePlan(context.Background(), wf)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	wf, err = engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if wf.State != domain.WorkflowStateCompleted {
		t.Fatalf("workflow state = %s, want completed", wf.State)
	}
	if wf.Tasks[0].Status != domain.WorkflowTaskDone {
		t.Fatalf("task status = %s, want done", wf.Tasks[0].Status)
	}
	if len(wf.RunHistory) < 3 {
		t.Fatalf("run history too short: %+v", wf.RunHistory)
	}
	if strings.TrimSpace(wf.ExecutionSummary) == "" {
		t.Fatalf("expected execution summary to be populated: %+v", wf)
	}
}

func TestEngineExecuteWorkflowWithToolCall(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Inspect README","planner_instructions":"Use repository tools"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"inspection","description":"inspect repo","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"inspection","title":"Read README","description":"Read the README and summarize the project name","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Project name identified"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"README.md"}`)}},
			{Kind: provider.EventDone, StopReason: domain.StopToolUse},
		},
		{
			{Kind: provider.EventText, Text: `{"status":"done","summary":"The project name is apex-code."}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"workflow completed"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 4}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-tools", "Inspect the README")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	wf, err = engine.ApprovePlan(context.Background(), wf)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	wf, err = engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if wf.State != domain.WorkflowStateCompleted {
		t.Fatalf("workflow state = %s, want completed", wf.State)
	}
	if wf.Tasks[0].Status != domain.WorkflowTaskDone {
		t.Fatalf("task status = %s, want done", wf.Tasks[0].Status)
	}
}

func TestEngineExecuteWorkflowFinalizesAfterMaxIterations(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Inspect architecture","planner_instructions":"Use repository tools"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"inspection","description":"inspect repo","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"inspection","title":"Analyze architecture","description":"Inspect the codebase and summarize the architecture","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Architecture summarized"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"README.md"}`)}},
			{Kind: provider.EventDone, StopReason: domain.StopToolUse},
		},
		{
			{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_2", Name: "read_file", Arguments: []byte(`{"path":"internal/codermode/engine.go","start_line":1,"end_line":120}`)}},
			{Kind: provider.EventDone, StopReason: domain.StopToolUse},
		},
		{
			{Kind: provider.EventText, Text: `{"status":"done","summary":"The architecture flows from CLI entrypoint to provider/tool wiring, then through coder orchestration and task execution."}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-finalize", "Inspect the architecture")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	wf, err = engine.ApprovePlan(context.Background(), wf)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	wf, err = engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if wf.State != domain.WorkflowStateCompleted {
		t.Fatalf("workflow state = %s, want completed", wf.State)
	}
	if wf.Tasks[0].Status != domain.WorkflowTaskDone {
		t.Fatalf("task status = %s, want done", wf.Tasks[0].Status)
	}
	if got := strings.Join(wf.Tasks[0].Outputs, " "); !strings.Contains(got, "architecture flows") {
		t.Fatalf("task outputs = %+v", wf.Tasks[0].Outputs)
	}
}

func TestEngineExecuteWorkflowAcceptsPlainTextFinalResponse(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Inspect README","planner_instructions":"Use repository tools"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"inspection","description":"inspect repo","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"inspection","title":"Read README","description":"Read the README and summarize it","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["README summarized"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `Read the README and summarized the project structure.`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-nonjson", "Inspect the README")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	wf, err = engine.ApprovePlan(context.Background(), wf)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	wf, err = engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if wf.Tasks[0].Status != domain.WorkflowTaskDone {
		t.Fatalf("task status = %s, want done", wf.Tasks[0].Status)
	}
	if got := strings.Join(wf.Tasks[0].Outputs, " "); !strings.Contains(got, "summarized the project structure") {
		t.Fatalf("task outputs = %+v", wf.Tasks[0].Outputs)
	}
}

func TestEngineExecuteWorkflowBlockedPrefixControlsTaskState(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Create file","planner_instructions":"Use tools"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"build","description":"do it","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"build","title":"Create file","description":"Create a file in the workspace","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["File created"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `BLOCKED: write_file was not called, so no file was created.`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-blocked", "Create file")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	wf, err = engine.ApprovePlan(context.Background(), wf)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	wf, err = engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if wf.Tasks[0].Status != domain.WorkflowTaskBlocked {
		t.Fatalf("task status = %s, want blocked", wf.Tasks[0].Status)
	}
}

func TestEnginePlannerAcceptsNumericPhaseTaskIDs(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Implement CI","planner_instructions":"Read README first"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"planning","description":"plan it","task_ids":[1,2]}],"tasks":[{"id":"1","phase":"planning","title":"Read README","description":"Read the README for project context","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Summarize the build requirements"],"outputs":[]},{"id":"2","phase":"planning","title":"Add CI workflow","description":"Create a GitHub Actions workflow to build the project","dependencies":["1"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Workflow builds the project"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-2", "I want a github workflow")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if len(wf.Tasks) != 2 || wf.Tasks[0].ID != "1" || wf.Tasks[1].ID != "2" {
		t.Fatalf("unexpected tasks after numeric phase ids: %+v", wf.Tasks)
	}
}

func TestEnginePlannerAcceptsNumericTaskIDs(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Implement CI","planner_instructions":"Read README first"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"planning","description":"plan it","task_ids":[1]}],"tasks":[{"id":1,"phase":"planning","title":"Read README","description":"Read the README for project context","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":[1,"Summarize the build requirements"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-4", "I want a github workflow")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if len(wf.Tasks) != 1 || wf.Tasks[0].ID != "1" {
		t.Fatalf("unexpected normalized task ids: %+v", wf.Tasks)
	}
	if len(wf.Tasks[0].AcceptanceCriteria) != 2 || wf.Tasks[0].AcceptanceCriteria[0] != "1" {
		t.Fatalf("unexpected normalized acceptance criteria: %+v", wf.Tasks[0].AcceptanceCriteria)
	}
}

func TestEnginePlannerAcceptsScalarTaskLists(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Implement CI","planner_instructions":"Read README first"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"planning","description":"plan it","task_ids":"read_readme"}],"tasks":[{"id":"read_readme","phase":"planning","title":"Read README","description":"Read the README for project context","dependencies":"","status":"pending","owner_agent":"architecture","acceptance_criteria":"Summarize the build requirements","outputs":""}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-5", "I want a github workflow")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if len(wf.Tasks) != 1 {
		t.Fatalf("unexpected task count: %+v", wf.Tasks)
	}
	if len(wf.Tasks[0].AcceptanceCriteria) != 1 || wf.Tasks[0].AcceptanceCriteria[0] != "Summarize the build requirements" {
		t.Fatalf("unexpected normalized acceptance criteria: %+v", wf.Tasks[0].AcceptanceCriteria)
	}
	if len(wf.Tasks[0].Dependencies) != 0 {
		t.Fatalf("expected empty dependencies after scalar normalization, got %+v", wf.Tasks[0].Dependencies)
	}
}

func TestEnginePersistsFailedPlanningWorkflow(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Implement CI","planner_instructions":"Read README first"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"broken","phases":[{"name":"planning","description":"plan it","task_ids":`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	_, err = engine.CreatePlan(context.Background(), "session-3", "I want a github workflow")
	if err == nil {
		t.Fatal("expected planning to fail")
	}
	items, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected failed workflow to be persisted, got %d items", len(items))
	}
	if items[0].State != domain.WorkflowStateFailed {
		t.Fatalf("workflow state = %s, want failed", items[0].State)
	}
	if len(items[0].RunHistory) == 0 || items[0].RunHistory[len(items[0].RunHistory)-1].Error == "" {
		t.Fatalf("expected failure recorded in run history: %+v", items[0].RunHistory)
	}
}

func TestEngineRepairsInvalidBackslashEscapesInJSONStrings(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"enriched_prompt":"Create abc\a.txt","planner_instructions":"Keep it simple"}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"build","description":"do it","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"build","title":"Write script","description":"Create abc\a.txt from a script","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["File is created"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-6", "Create abc\\a.txt")
	if err != nil {
		t.Fatalf("create plan should repair invalid escapes: %v", err)
	}
	if !strings.Contains(wf.EnrichedPrompt, `abc\a.txt`) {
		t.Fatalf("expected repaired enriched prompt to preserve backslash content, got %q", wf.EnrichedPrompt)
	}
	if len(wf.Tasks) != 1 || !strings.Contains(wf.Tasks[0].Description, `abc\a.txt`) {
		t.Fatalf("expected repaired task description, got %+v", wf.Tasks)
	}
}
