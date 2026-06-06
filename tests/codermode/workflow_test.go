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
	if got.Stages.Planner.Input != "build coder mode" {
		t.Fatalf("expected initial planner input seeded in workflow json, got %+v", got.Stages)
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
	if wf.Stages.Planner.Output == "" {
		t.Fatalf("expected raw stage outputs to be stored in workflow: %+v", wf.Stages)
	}
	if wf.Stages.Planner.Input == "" {
		t.Fatalf("expected planner input to be stored in workflow: %+v", wf.Stages)
	}
	if !strings.Contains(strings.ToLower(wf.Stages.Planner.Input), "build coder mode") {
		t.Fatalf("planner input missing expected content: %q", wf.Stages.Planner.Input)
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
	if len(wf.RunHistory) < 2 {
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

func TestEngineExecuteMutationTaskWithoutWriteEvidenceBecomesBlocked(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"build","description":"do it","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"build","title":"Create sample.json","description":"Create sample.json in the workspace","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["sample.json exists"],"outputs":["sample.json"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `I am about to create sample.json.`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-mutation-proof", "Create sample file")
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
	if wf.State != domain.WorkflowStateFailed {
		t.Fatalf("workflow state = %s, want failed", wf.State)
	}
	if got := strings.Join(wf.Tasks[0].Outputs, " "); !strings.Contains(got, "no successful file mutation evidence") {
		t.Fatalf("task outputs = %+v", wf.Tasks[0].Outputs)
	}
}

func TestEngineExecuteResearchTaskWithScratchpadOutputDoesNotRequireMutation(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"research","description":"inspect format","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"research","title":"Research Toon format","description":"Inspect the Toon repository and summarize the format rules.","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Format rules summarized"],"outputs":["Summary of Toon format structure written to memory/scratchpad."]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `status=done

Summarized the Toon format structure and encoder expectations from the repository evidence.`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-research-summary", "Research the Toon format")
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
}

func TestEngineEmbeddedBlockedMarkerControlsTaskState(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"build","description":"do it","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"build","title":"Write converter","description":"Write convert-toon.js","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["converter exists"],"outputs":["convert-toon.js"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `I analyzed the task and tried a few things. **BLOCKED:** convert-toon.js was never created in the workspace.`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-embedded-blocked", "Write converter")
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

func TestEnginePseudoToolMarkupFinalResponseBecomesBlocked(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"build","description":"do it","task_ids":["t1"]}],"tasks":[{"id":"t1","phase":"build","title":"Create package.json","description":"Create package.json in the workspace.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["package.json exists"],"outputs":["package.json"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: "I will create the file.\n\n<｜｜DSML｜｜tool_calls>\n<｜｜DSML｜｜invoke name=\"write_file\">\n</｜｜DSML｜｜invoke>\n</｜｜DSML｜｜tool_calls>"},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-pseudo-tools", "Create package.json")
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
	if wf.State != domain.WorkflowStateFailed {
		t.Fatalf("workflow state = %s, want failed", wf.State)
	}
	if got := strings.Join(wf.Tasks[0].Outputs, " "); !strings.Contains(got, "pseudo-tool markup") {
		t.Fatalf("task outputs = %+v", wf.Tasks[0].Outputs)
	}
}

func TestEnginePlannerAcceptsNumericPhaseTaskIDs(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
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

func TestEnginePlannerRepairsOversegmentedSimplePlan(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Create a PowerShell countdown script","phases":[{"name":"Design and Planning","description":"Define the script requirements, architecture, and test plan.","task_ids":["task-1","task-2","task-3"]},{"name":"Implementation","description":"Write the PowerShell script and verify its functionality.","task_ids":["task-4","task-5"]},{"name":"Review and Validation","description":"Review script code and validate against acceptance criteria.","task_ids":["task-6"]}],"tasks":[{"id":"task-1","phase":"Design and Planning","title":"Define script requirements","description":"Clarify that script must countdown from 10 to 1.","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Requirements documented"],"outputs":[]},{"id":"task-2","phase":"Design and Planning","title":"Design script architecture","description":"Design the script structure.","dependencies":["task-1"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Architecture design completed"],"outputs":[]},{"id":"task-3","phase":"Design and Planning","title":"Plan tests for countdown script","description":"Define test cases for countdown behavior.","dependencies":["task-1"],"status":"pending","owner_agent":"tester","acceptance_criteria":["Test plan defined"],"outputs":[]},{"id":"task-4","phase":"Implementation","title":"Implement PowerShell countdown script","description":"Write the script with loop, sleep, and final message.","dependencies":["task-2"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Script runs"],"outputs":[]},{"id":"task-5","phase":"Implementation","title":"Unit test the countdown script","description":"Execute script and verify countdown output, timing, and final message.","dependencies":["task-4"],"status":"pending","owner_agent":"tester","acceptance_criteria":["All test cases pass"],"outputs":[]},{"id":"task-6","phase":"Review and Validation","title":"Review script and test results","description":"Review the script code for correctness and validate test results.","dependencies":["task-5"],"status":"pending","owner_agent":"reviewer","acceptance_criteria":["Approved"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Create the countdown script and verify it","phases":[{"name":"implementation","description":"build and verify the script","task_ids":["task-1","task-2"]}],"tasks":[{"id":"task-1","phase":"implementation","title":"Create countdown script","description":"Create a PowerShell script that counts down from 10 to 1 with a one-second pause and a final completion message.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Script exists and runs"],"outputs":[]},{"id":"task-2","phase":"implementation","title":"Verify countdown behavior","description":"Run the script and verify the countdown output and final message.","dependencies":["task-1"],"status":"pending","owner_agent":"tester","acceptance_criteria":["Observed output matches the requested countdown"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-simple-repair", "Create a powershell script here which when run starts a countdown from 10 to 1")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if len(wf.Tasks) != 2 {
		t.Fatalf("expected repaired compact plan, got %+v", wf.Tasks)
	}
	if wf.Tasks[0].Title != "Create countdown script" || wf.Tasks[1].OwnerAgent != domain.WorkflowAgentTester {
		t.Fatalf("unexpected repaired plan: %+v", wf.Tasks)
	}
	if !strings.Contains(wf.Stages.Planner.Input, "previous plan was not acceptable") {
		t.Fatalf("expected repaired planner input to include retry guidance, got %q", wf.Stages.Planner.Input)
	}
}

func TestEnginePlannerRepairsOversizedSmallArtifactPlan(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Create converter project","phases":[{"name":"Research and Setup","description":"Explore the target repository and set up the project.","task_ids":["research-repo","init-project"]},{"name":"Implementation","description":"Build the converter script, sample, and documentation.","task_ids":["build-converter","create-sample","write-readme"]}],"tasks":[{"id":"research-repo","phase":"Research and Setup","title":"Research Toon repository structure and format","description":"Clone or inspect https://github.com/toon-format/toon to understand the Toon file format specification.","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Format understood"],"outputs":["Summary of Toon format structure written to memory/scratchpad."]},{"id":"init-project","phase":"Research and Setup","title":"Initialize project files","description":"Create the folder and add package.json.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["package.json exists"],"outputs":["package.json"]},{"id":"build-converter","phase":"Implementation","title":"Write converter script","description":"Create convert-to-toon.js that transforms JSON into Toon format.","dependencies":["research-repo","init-project"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Script exists"],"outputs":["convert-to-toon.js"]},{"id":"create-sample","phase":"Implementation","title":"Create sample.json","description":"Write sample.json for the converter.","dependencies":["research-repo"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Sample exists"],"outputs":["sample.json"]},{"id":"write-readme","phase":"Implementation","title":"Write README","description":"Create README.md explaining usage.","dependencies":["build-converter","create-sample"],"status":"pending","owner_agent":"reviewer","acceptance_criteria":["README exists"],"outputs":["README.md"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Create the converter files and document usage","phases":[{"name":"implementation","description":"build and verify the requested files","task_ids":["task-1","task-2"]}],"tasks":[{"id":"task-1","phase":"implementation","title":"Create converter project files","description":"In the current folder, create the converter JavaScript script, sample.json, package.json if needed, and any other required implementation files using the Toon repository format as reference.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Requested files exist and reflect the Toon format"],"outputs":["convert-to-toon.js","sample.json","package.json"]},{"id":"task-2","phase":"implementation","title":"Write README and verify usage","description":"Write README.md with the usage command and verify the converter behavior against sample.json.","dependencies":["task-1"],"status":"pending","owner_agent":"tester","acceptance_criteria":["README exists and the converter output was verified"],"outputs":["README.md"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-artifact-repair", "Read this repo and in this folder create a JS script, sample.json, README, and package.json if needed")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if len(wf.Tasks) != 2 {
		t.Fatalf("expected repaired compact plan, got %+v", wf.Tasks)
	}
	if wf.Tasks[0].OwnerAgent != domain.WorkflowAgentSolutioner || wf.Tasks[1].OwnerAgent != domain.WorkflowAgentTester {
		t.Fatalf("unexpected repaired plan owners: %+v", wf.Tasks)
	}
	if !strings.Contains(wf.Stages.Planner.Input, "previous plan was not acceptable") {
		t.Fatalf("expected repaired planner input to include retry guidance, got %q", wf.Stages.Planner.Input)
	}
}

func TestEnginePlannerRepairsRepoLearningIntoResearchThenImplementation(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Create sample usage files","phases":[{"name":"implementation","description":"do it","task_ids":["task-1"]}],"tasks":[{"id":"task-1","phase":"implementation","title":"Create sample .toon.json and usage .js files","description":"Read the README of https://github.com/toon-format/toon to understand usage, then create sample JSON and JS files showing its usage.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["sample files exist"],"outputs":["sample.json","sample.js"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Research usage and then create sample files","phases":[{"name":"research","description":"extract usage facts","task_ids":["task-1"]},{"name":"implementation","description":"create artifacts","task_ids":["task-2"]}],"tasks":[{"id":"task-1","phase":"research","title":"Read the Toon README and summarize usage facts","description":"Inspect the Toon repository README and extract the exact usage patterns, file formats, and library/API examples needed to create a correct sample.","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Usage facts summarized from repo evidence"],"outputs":["Usage summary written to memory/scratchpad."]},{"id":"task-2","phase":"implementation","title":"Create sample JSON and JS usage files","description":"Using the research handoff, create a sample JSON file and JavaScript example that correctly demonstrate the Toon project usage.","dependencies":["task-1"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Sample files exist and reflect the README usage"],"outputs":["sample.json","sample.js"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-repo-learning-repair", "Read the readme of this project: https://github.com/toon-format/toon to know how to use it, create a sample json and js code to show its usage")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if len(wf.Tasks) != 2 {
		t.Fatalf("expected repaired 2-task plan, got %+v", wf.Tasks)
	}
	if wf.Tasks[0].OwnerAgent != domain.WorkflowAgentArchitecture || wf.Tasks[1].OwnerAgent != domain.WorkflowAgentSolutioner {
		t.Fatalf("unexpected repaired owners: %+v", wf.Tasks)
	}
	if !strings.Contains(wf.Stages.Planner.Input, "previous plan was not acceptable") {
		t.Fatalf("expected repaired planner input to include retry guidance, got %q", wf.Stages.Planner.Input)
	}
}

func TestEnginePlannerRetriesMultipleRepairsBeforeAcceptingPlan(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Create sample usage files","phases":[{"name":"implementation","description":"do it","task_ids":["task-1"]}],"tasks":[{"id":"task-1","phase":"implementation","title":"Create sample .toon.json and usage .js files","description":"Read the README of https://github.com/toon-format/toon to understand usage, then create sample JSON and JS files showing its usage.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["sample files exist"],"outputs":["sample.json","sample.js"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Still wrong","phases":[{"name":"implementation","description":"do it","task_ids":["task-1","task-2"]}],"tasks":[{"id":"task-1","phase":"implementation","title":"Read Toon README and create sample files","description":"Read the README and immediately create sample.json and sample.js.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["sample files exist"],"outputs":["sample.json"]},{"id":"task-2","phase":"implementation","title":"Review files","description":"Review the created files.","dependencies":["task-1"],"status":"pending","owner_agent":"reviewer","acceptance_criteria":["review complete"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"summary":"Research usage and then create sample files","phases":[{"name":"research","description":"extract usage facts","task_ids":["task-1"]},{"name":"implementation","description":"create artifacts","task_ids":["task-2"]}],"tasks":[{"id":"task-1","phase":"research","title":"Read the Toon README and summarize usage facts","description":"Inspect the Toon repository README and extract the exact usage patterns, file formats, and library/API examples needed to create a correct sample.","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["Usage facts summarized from repo evidence"],"outputs":["Usage summary written to memory/scratchpad."]},{"id":"task-2","phase":"implementation","title":"Create sample JSON and JS usage files","description":"Using the research handoff, create a sample JSON file and JavaScript example that correctly demonstrate the Toon project usage.","dependencies":["task-1"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["Sample files exist and reflect the README usage"],"outputs":["sample.json","sample.js"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-repo-learning-multi-repair", "Read the readme of this project: https://github.com/toon-format/toon to know how to use it, create a sample json and js code to show its usage")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if len(wf.Tasks) != 2 {
		t.Fatalf("expected repaired 2-task plan, got %+v", wf.Tasks)
	}
	if wf.Tasks[0].OwnerAgent != domain.WorkflowAgentArchitecture || wf.Tasks[1].OwnerAgent != domain.WorkflowAgentSolutioner {
		t.Fatalf("unexpected repaired owners: %+v", wf.Tasks)
	}
	if count := strings.Count(wf.Stages.Planner.Input, "not acceptable"); count < 2 {
		t.Fatalf("expected planner input to show repeated repair guidance, got %q", wf.Stages.Planner.Input)
	}
}

func TestEngineMutationTaskAssignedToArchitectureIsNormalizedToSolutioner(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Create converter files","phases":[{"name":"implementation","description":"do the work","task_ids":["task-1"]}],"tasks":[{"id":"task-1","phase":"implementation","title":"Create converter script and companion files","description":"In the current folder, create toonify.js, sample.json, README.md, and package.json if needed.","dependencies":[],"status":"pending","owner_agent":"architecture","acceptance_criteria":["toonify.js reads sample.json and outputs valid Toon format"],"outputs":["toonify.js","sample.json","README.md","package.json"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-owner-fix", "\x00\x00Read repo and create script files here")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if wf.Tasks[0].OwnerAgent != domain.WorkflowAgentSolutioner {
		t.Fatalf("task owner = %s, want solutioner", wf.Tasks[0].OwnerAgent)
	}
	if strings.Contains(wf.UserPrompt, "\x00") || strings.Contains(wf.Stages.Planner.Input, "\x00") {
		t.Fatalf("expected prompt sanitization, got workflow prompt %q planner input %q", wf.UserPrompt, wf.Stages.Planner.Input)
	}
}

func TestEnginePlannerRejectsUnknownDependenciesAndMissingPhaseReferences(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Bad plan","phases":[{"name":"implementation","description":"do it","task_ids":["task-1"]}],"tasks":[{"id":"task-1","phase":"implementation","title":"Create sample file","description":"Create sample.json.","dependencies":["task-2"],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["sample exists"],"outputs":["sample.json"]},{"id":"task-2","phase":"implementation","title":"Write README","description":"Write README.md.","dependencies":[],"status":"pending","owner_agent":"solutioner","acceptance_criteria":["README exists"],"outputs":["README.md"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	_, err = engine.CreatePlan(context.Background(), "session-bad-graph", "Create sample and readme files")
	if err == nil {
		t.Fatalf("expected planner validation error")
	}
	if !strings.Contains(err.Error(), "not referenced by any phase") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestEnginePlannerAcceptsNumericTaskIDs(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
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

func TestEnginePlannerNormalizesStageAgentsToWorkerAgents(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Plan ready","phases":[{"name":"build","description":"do it","task_ids":["t1","t2","t3"]}],"tasks":[{"id":"t1","phase":"build","title":"Inspect architecture","description":"Analyze the architecture before edits","dependencies":[],"status":"pending","owner_agent":"planner","acceptance_criteria":["Architecture understood"],"outputs":[]},{"id":"t2","phase":"build","title":"Create file","description":"Write the implementation file","dependencies":["t1"],"status":"pending","owner_agent":"orchestrator","acceptance_criteria":["File created"],"outputs":[]},{"id":"t3","phase":"build","title":"Run tests","description":"Verify the feature with tests","dependencies":["t2"],"status":"pending","owner_agent":"planner","acceptance_criteria":["Tests pass"],"outputs":[]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	wf, err := engine.CreatePlan(context.Background(), "session-stage-owner", "create a file")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if got := wf.Tasks[0].OwnerAgent; got != domain.WorkflowAgentArchitecture {
		t.Fatalf("task 1 owner = %s, want architecture", got)
	}
	if got := wf.Tasks[1].OwnerAgent; got != domain.WorkflowAgentSolutioner {
		t.Fatalf("task 2 owner = %s, want solutioner", got)
	}
	if got := wf.Tasks[2].OwnerAgent; got != domain.WorkflowAgentTester {
		t.Fatalf("task 3 owner = %s, want tester", got)
	}
}

func TestEnginePersistsFailedPlanningWorkflow(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
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

func TestEngineRejectsWrongPlannerJSONShape(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: "```json\n{\"summary\":\"wrong shape\"}\n```"},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{MaxIterations: 2}
	})
	_, err = engine.CreatePlan(context.Background(), "session-wrong-shape", "Create arch.md")
	if err == nil || !strings.Contains(err.Error(), "planner returned no tasks") {
		t.Fatalf("expected wrong planner shape error, got %v", err)
	}
	items, listErr := store.List(context.Background())
	if listErr != nil {
		t.Fatalf("list workflows: %v", listErr)
	}
	if len(items) != 1 || items[0].State != domain.WorkflowStateFailed {
		t.Fatalf("expected failed workflow persisted, got %+v", items)
	}
}

func TestEngineRepairsInvalidBackslashEscapesInJSONStrings(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
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
	if !strings.Contains(wf.Stages.Planner.Input, `abc\a.txt`) {
		t.Fatalf("expected planner input to preserve backslash content, got %q", wf.Stages.Planner.Input)
	}
	if len(wf.Tasks) != 1 || !strings.Contains(wf.Tasks[0].Description, `abc\a.txt`) {
		t.Fatalf("expected repaired task description, got %+v", wf.Tasks)
	}
}
