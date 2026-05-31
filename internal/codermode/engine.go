package codermode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
)

type ProgressEvent struct {
	Kind     string
	Agent    domain.WorkflowAgent
	TaskID   string
	TaskTitle string
	Summary  string
	Error    string
	Workflow domain.CoderWorkflow
}

type Engine struct {
	provider provider.Provider
	tools    agent.ToolDispatcher
	store    *Store
	options  func() agent.Options
}

func NewEngine(p provider.Provider, tools agent.ToolDispatcher, store *Store, options func() agent.Options) *Engine {
	return &Engine{provider: p, tools: tools, store: store, options: options}
}

func (e *Engine) CreatePlan(ctx context.Context, sessionID, prompt string) (domain.CoderWorkflow, error) {
	wf := NewWorkflow(sessionID, prompt)
	if e.store != nil {
		if err := e.store.Save(ctx, wf); err != nil {
			return domain.CoderWorkflow{}, err
		}
	}
	enriched, plannerNotes, rawOrchestrator, err := e.runOrchestrator(ctx, wf)
	if err != nil {
		wf.State = domain.WorkflowStateFailed
		wf.Stages.Orchestrator.Status = "failed"
		wf.Stages.Orchestrator.Error = err.Error()
		wf.Stages.Orchestrator.UpdatedAt = time.Now().UTC()
		appendRun(&wf, domain.WorkflowAgentRun{
			ID:          runID(),
			Agent:       domain.WorkflowAgentOrchestrator,
			Reason:      "enrich initial coder request",
			Input:       prompt,
			Error:       err.Error(),
			StartedAt:   time.Now().UTC(),
			CompletedAt: time.Now().UTC(),
		})
		if e.store != nil {
			_ = e.store.Save(ctx, wf)
		}
		return domain.CoderWorkflow{}, err
	}
	wf.Stages.Orchestrator.Status = "done"
	wf.Stages.Orchestrator.Input = prompt
	wf.Stages.Orchestrator.Output = rawOrchestrator
	wf.Stages.Orchestrator.Error = ""
	wf.Stages.Orchestrator.UpdatedAt = time.Now().UTC()
	wf.EnrichedPrompt = enriched
	wf.PlannerInstructions = plannerNotes
	wf.State = domain.WorkflowStatePlanReview
	plan, rawPlan, plannerInput, err := e.runPlanner(ctx, wf, "")
	if err != nil {
		wf.State = domain.WorkflowStateFailed
		wf.Stages.Planner.Status = "failed"
		wf.Stages.Planner.Input = plannerInput
		wf.Stages.Planner.Error = err.Error()
		wf.Stages.Planner.UpdatedAt = time.Now().UTC()
		appendRun(&wf, domain.WorkflowAgentRun{
			ID:          runID(),
			Agent:       domain.WorkflowAgentPlanner,
			Reason:      "draft initial plan",
			Input:       plannerNotes,
			Error:       err.Error(),
			StartedAt:   time.Now().UTC(),
			CompletedAt: time.Now().UTC(),
		})
		if e.store != nil {
			_ = e.store.Save(ctx, wf)
		}
		return domain.CoderWorkflow{}, err
	}
	wf.Stages.Planner.Status = "done"
	wf.Stages.Planner.Input = plannerInput
	wf.Stages.Planner.Output = rawPlan
	wf.Stages.Planner.Error = ""
	wf.Stages.Planner.UpdatedAt = time.Now().UTC()
	wf.Tasks = flattenPlan(plan)
	markReadyTasks(&wf)
	appendRun(&wf, domain.WorkflowAgentRun{
		ID:          runID(),
		Agent:       domain.WorkflowAgentOrchestrator,
		Reason:      "enrich initial coder request",
		Input:       prompt,
		Output:      enriched,
		StartedAt:   time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
	})
	appendRun(&wf, domain.WorkflowAgentRun{
		ID:          runID(),
		Agent:       domain.WorkflowAgentPlanner,
		Reason:      "draft initial plan",
		Input:       plannerNotes,
		Output:      rawPlan,
		Structured:  map[string]any{"tasks": len(wf.Tasks)},
		StartedAt:   time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
	})
	if e.store != nil {
		if err := e.store.Save(ctx, wf); err != nil {
			return domain.CoderWorkflow{}, err
		}
	}
	return wf, nil
}

func (e *Engine) RevisePlan(ctx context.Context, wf domain.CoderWorkflow, revision string) (domain.CoderWorkflow, error) {
	original := cloneWorkflow(wf)
	plan, rawPlan, plannerInput, err := e.runPlanner(ctx, wf, revision)
	if err != nil {
		wf.Stages.Planner.Status = "failed"
		wf.Stages.Planner.Input = plannerInput
		wf.Stages.Planner.Error = err.Error()
		wf.Stages.Planner.UpdatedAt = time.Now().UTC()
		if e.store != nil {
			_ = e.store.Save(ctx, wf)
		}
		return wf, err
	}
	wf.PlanVersion++
	wf.Stages.Planner.Status = "done"
	wf.Stages.Planner.Input = plannerInput
	wf.Stages.Planner.Output = rawPlan
	wf.Stages.Planner.Error = ""
	wf.Stages.Planner.UpdatedAt = time.Now().UTC()
	wf.Tasks = flattenPlan(plan)
	for i := range wf.Tasks {
		if wf.Tasks[i].Status == "" {
			wf.Tasks[i].Status = domain.WorkflowTaskPending
		}
	}
	wf.State = domain.WorkflowStatePlanReview
	wf.ActiveTaskID = ""
	wf.Mutations = append(wf.Mutations, domain.WorkflowMutation{
		ID:          mutationID(),
		Type:        domain.WorkflowMutationUpdate,
		Description: "planner revised the workflow plan",
		Agent:       domain.WorkflowAgentPlanner,
		CreatedAt:   time.Now().UTC(),
		Before:      map[string]any{"tasks": len(original.Tasks), "plan_version": original.PlanVersion},
		After:       map[string]any{"tasks": len(wf.Tasks), "plan_version": wf.PlanVersion},
	})
	markReadyTasks(&wf)
	appendRun(&wf, domain.WorkflowAgentRun{
		ID:          runID(),
		Agent:       domain.WorkflowAgentPlanner,
		Reason:      "revise plan",
		Input:       revision,
		Output:      rawPlan,
		Structured:  map[string]any{"plan_version": wf.PlanVersion},
		StartedAt:   time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
	})
	if e.store != nil {
		if err := e.store.Save(ctx, wf); err != nil {
			return wf, err
		}
	}
	return wf, nil
}

func (e *Engine) ApprovePlan(ctx context.Context, wf domain.CoderWorkflow) (domain.CoderWorkflow, error) {
	wf.State = domain.WorkflowStateApproved
	wf.UpdatedAt = time.Now().UTC()
	markReadyTasks(&wf)
	if e.store != nil {
		if err := e.store.Save(ctx, wf); err != nil {
			return wf, err
		}
	}
	return wf, nil
}

func (e *Engine) Execute(ctx context.Context, wf domain.CoderWorkflow) (domain.CoderWorkflow, error) {
	return e.execute(ctx, wf, nil)
}

func (e *Engine) ExecuteStream(ctx context.Context, wf domain.CoderWorkflow, onProgress func(ProgressEvent)) (domain.CoderWorkflow, error) {
	return e.execute(ctx, wf, onProgress)
}

func (e *Engine) execute(ctx context.Context, wf domain.CoderWorkflow, onProgress func(ProgressEvent)) (domain.CoderWorkflow, error) {
	if wf.State != domain.WorkflowStateApproved && wf.State != domain.WorkflowStateExecuting && wf.State != domain.WorkflowStatePaused {
		return wf, fmt.Errorf("workflow is not approved for execution")
	}
	wf.State = domain.WorkflowStateExecuting
	for {
		markReadyTasks(&wf)
		task, ok := nextRunnableTask(wf)
		if !ok {
			if allDone(wf) {
				wf.State = domain.WorkflowStateCompleted
			} else {
				wf.State = domain.WorkflowStatePaused
			}
			break
		}
		idx := findTaskIndex(wf, task.ID)
		wf.ActiveTaskID = task.ID
		wf.ActiveAgent = task.OwnerAgent
		wf.Tasks[idx].Status = domain.WorkflowTaskRunning
		emitProgress(onProgress, ProgressEvent{
			Kind:      "started",
			Agent:     task.OwnerAgent,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Workflow:  cloneWorkflow(wf),
		})
		if e.store != nil {
			if err := e.store.Save(ctx, wf); err != nil {
				return wf, err
			}
		}
		updated, err := e.executeTask(ctx, wf, task)
		if err != nil {
			wf.Tasks[idx].Status = domain.WorkflowTaskBlocked
			wf.State = domain.WorkflowStateFailed
			appendRun(&wf, domain.WorkflowAgentRun{
				ID:          runID(),
				Agent:       task.OwnerAgent,
				Reason:      "execute task",
				TaskID:      task.ID,
				Error:       err.Error(),
				StartedAt:   time.Now().UTC(),
				CompletedAt: time.Now().UTC(),
			})
			wf.ActiveTaskID = ""
			wf.ActiveAgent = ""
			_ = e.finalizeWorkflow(&wf)
			emitProgress(onProgress, ProgressEvent{
				Kind:      "failed",
				Agent:     task.OwnerAgent,
				TaskID:    task.ID,
				TaskTitle: task.Title,
				Error:     err.Error(),
				Workflow:  cloneWorkflow(wf),
			})
			if e.store != nil {
				_ = e.store.Save(ctx, wf)
			}
			return wf, err
		}
		wf = updated
		emitProgress(onProgress, ProgressEvent{
			Kind:      "completed",
			Agent:     task.OwnerAgent,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Summary:   latestTaskOutput(wf, task.ID),
			Workflow:  cloneWorkflow(wf),
		})
		if wf.State == domain.WorkflowStateReplanning || wf.State == domain.WorkflowStatePlanReview {
			break
		}
	}
	wf.UpdatedAt = time.Now().UTC()
	wf.ActiveTaskID = ""
	wf.ActiveAgent = ""
	_ = e.finalizeWorkflow(&wf)
	if e.store != nil {
		if err := e.store.Save(ctx, wf); err != nil {
			return wf, err
		}
	}
	return wf, nil
}

func (e *Engine) Load(ctx context.Context, id string) (domain.CoderWorkflow, error) {
	if e.store == nil {
		return domain.CoderWorkflow{}, fmt.Errorf("workflow store is disabled")
	}
	return e.store.Load(ctx, id)
}

func (e *Engine) LatestBySession(ctx context.Context, sessionID string) (domain.CoderWorkflow, bool, error) {
	if e.store == nil {
		return domain.CoderWorkflow{}, false, nil
	}
	return e.store.LatestBySession(ctx, sessionID)
}

func (e *Engine) runOrchestrator(ctx context.Context, wf domain.CoderWorkflow) (string, string, string, error) {
	type orchestratorOutput struct {
		EnrichedPrompt      string `json:"enriched_prompt"`
		PlannerInstructions string `json:"planner_instructions"`
	}
	out := orchestratorOutput{}
	raw, err := e.runJSONTurn(ctx, []domain.Message{
		{Role: domain.RoleSystem, Content: orchestratorPrompt},
		{Role: domain.RoleUser, Content: wf.UserPrompt},
	}, &out)
	if err != nil {
		return "", "", raw, err
	}
	if strings.TrimSpace(out.EnrichedPrompt) == "" {
		out.EnrichedPrompt = strings.TrimSpace(raw)
	}
	return out.EnrichedPrompt, out.PlannerInstructions, raw, nil
}

func (e *Engine) runPlanner(ctx context.Context, wf domain.CoderWorkflow, revision string) (domain.PlannerPlan, string, string, error) {
	var out domain.PlannerPlan
	input := wf.EnrichedPrompt
	if strings.TrimSpace(input) == "" {
		input = wf.UserPrompt
	}
	if strings.TrimSpace(revision) != "" {
		input += "\n\nPlan revision request:\n" + strings.TrimSpace(revision)
	}
	raw, err := e.runJSONTurn(ctx, []domain.Message{
		{Role: domain.RoleSystem, Content: plannerPrompt},
		{Role: domain.RoleUser, Content: input},
	}, &out)
	if err != nil {
		return domain.PlannerPlan{}, raw, input, err
	}
	normalizePlannerPlan(&out)
	for i := range out.Tasks {
		if out.Tasks[i].Status == "" {
			out.Tasks[i].Status = domain.WorkflowTaskPending
		}
	}
	return out, raw, input, nil
}

func (e *Engine) executeTask(ctx context.Context, wf domain.CoderWorkflow, task domain.WorkflowTask) (domain.CoderWorkflow, error) {
	systemPrompt := executionPrompt(task.OwnerAgent)
	if systemPrompt == "" {
		systemPrompt = solutionerPrompt
	}
	opts := e.options()
	messages := []domain.Message{
		{Role: domain.RoleSystem, Content: systemPrompt},
		{Role: domain.RoleSystem, Content: "Workflow execution context:\n" + taskExecutionContext(wf, task)},
		{Role: domain.RoleUser, Content: fmt.Sprintf("Execute task %s: %s\n\nDescription:\n%s", task.ID, task.Title, task.Description)},
	}
	startedAt := time.Now().UTC()
	state, err := agent.New(e.provider, e.tools).Run(ctx, messages, opts)
	if err != nil {
		return wf, err
	}
	completedAt := time.Now().UTC()
	if state.FinalResponse == nil {
		return wf, fmt.Errorf("task %s ended without a final response", task.ID)
	}
	var out executionOutput
	raw := state.FinalResponse.Message.Content
	if err := decodeJSONObject(raw, &out); err != nil {
		out.Status = "done"
		out.Summary = strings.TrimSpace(raw)
	}
	idx := findTaskIndex(wf, task.ID)
	if idx >= 0 {
		switch strings.ToLower(strings.TrimSpace(out.Status)) {
		case "blocked", "failed":
			wf.Tasks[idx].Status = domain.WorkflowTaskBlocked
			wf.State = domain.WorkflowStatePaused
		case "needs_replan", "replan":
			wf.Tasks[idx].Status = domain.WorkflowTaskNeedsPlan
			wf.State = domain.WorkflowStatePlanReview
		default:
			wf.Tasks[idx].Status = domain.WorkflowTaskDone
		}
		if strings.TrimSpace(out.Summary) != "" {
			wf.Tasks[idx].Outputs = append(wf.Tasks[idx].Outputs, out.Summary)
		}
	}
	if len(out.PlanMutations) > 0 {
		for _, mutation := range out.PlanMutations {
			mutation.ID = mutationID()
			mutation.Agent = task.OwnerAgent
			mutation.CreatedAt = time.Now().UTC()
			wf.Mutations = append(wf.Mutations, mutation)
		}
		wf.PlanVersion++
	}
	appendRun(&wf, domain.WorkflowAgentRun{
		ID:              runID(),
		Agent:           task.OwnerAgent,
		Reason:          "execute task",
		TaskID:          task.ID,
		Input:           task.Description,
		Output:          raw,
		RequestedReplan: wf.State == domain.WorkflowStatePlanReview,
		Structured: map[string]any{
			"turns":       len(state.Turns),
			"termination": string(state.TerminationReason),
			"summary":     out.Summary,
		},
		PromptTokens:     sumPromptTokens(state),
		CompletionTokens: sumCompletionTokens(state),
		TotalTokens:      sumTotalTokens(state),
		DurationMs:       completedAt.Sub(startedAt).Milliseconds(),
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
	})
	markReadyTasks(&wf)
	if allDone(wf) {
		wf.State = domain.WorkflowStateCompleted
	}
	return wf, nil
}

func (e *Engine) finalizeWorkflow(wf *domain.CoderWorkflow) error {
	if wf == nil {
		return nil
	}
	wf.ExecutionSummary = buildExecutionSummary(*wf)
	return nil
}

type executionOutput struct {
	Status        string                    `json:"status"`
	Summary       string                    `json:"summary"`
	PlanMutations []domain.WorkflowMutation `json:"plan_mutations,omitempty"`
}

func (e *Engine) runJSONTurn(ctx context.Context, messages []domain.Message, target any) (string, error) {
	stream, err := e.provider.Complete(ctx, domain.Request{
		Model:    "",
		Messages: messages,
	})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var text strings.Builder
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if ev.Kind == provider.EventText {
			text.WriteString(ev.Text)
		}
	}
	raw := strings.TrimSpace(text.String())
	if err := decodeJSONObject(raw, target); err != nil {
		return raw, err
	}
	return raw, nil
}

func decodeJSONObject(raw string, target any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty json response")
	}
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end >= start {
		raw = raw[start : end+1]
	}
	if err := json.Unmarshal([]byte(raw), target); err == nil {
		return nil
	}
	return json.Unmarshal([]byte(repairJSONStringEscapes(raw)), target)
}

func repairJSONStringEscapes(raw string) string {
	var out strings.Builder
	out.Grow(len(raw) + 8)
	inString := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' && !isEscapedQuote(raw, i) {
			inString = !inString
			out.WriteByte(ch)
			continue
		}
		if inString && ch == '\\' {
			if i+1 >= len(raw) {
				out.WriteString(`\\`)
				continue
			}
			next := raw[i+1]
			if isValidJSONEscape(next, raw, i+1) {
				out.WriteByte(ch)
				continue
			}
			out.WriteString(`\\`)
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func isEscapedQuote(raw string, quoteIndex int) bool {
	backslashes := 0
	for i := quoteIndex - 1; i >= 0 && raw[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func isValidJSONEscape(next byte, raw string, nextIndex int) bool {
	switch next {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	case 'u':
		if nextIndex+4 >= len(raw) {
			return false
		}
		for i := nextIndex + 1; i <= nextIndex+4; i++ {
			if !isHex(raw[i]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isHex(ch byte) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

func normalizePlannerPlan(plan *domain.PlannerPlan) {
	for i := range plan.Phases {
		for j := range plan.Phases[i].TaskIDs {
			plan.Phases[i].TaskIDs[j] = strings.TrimSpace(plan.Phases[i].TaskIDs[j])
		}
	}
	for i := range plan.Tasks {
		plan.Tasks[i].ID = strings.TrimSpace(plan.Tasks[i].ID)
		plan.Tasks[i].Phase = strings.TrimSpace(plan.Tasks[i].Phase)
		plan.Tasks[i].Title = strings.TrimSpace(plan.Tasks[i].Title)
		plan.Tasks[i].Description = strings.TrimSpace(plan.Tasks[i].Description)
		if plan.Tasks[i].OwnerAgent == "" {
			plan.Tasks[i].OwnerAgent = domain.WorkflowAgentSolutioner
		}
		for j := range plan.Tasks[i].Dependencies {
			plan.Tasks[i].Dependencies[j] = strings.TrimSpace(plan.Tasks[i].Dependencies[j])
		}
	}
}

func workflowJSON(wf domain.CoderWorkflow) string {
	data, _ := json.MarshalIndent(wf, "", "  ")
	return string(data)
}

func taskExecutionContext(wf domain.CoderWorkflow, task domain.WorkflowTask) string {
	type contextTask struct {
		ID           string                    `json:"id"`
		Title        string                    `json:"title"`
		Status       domain.WorkflowTaskStatus `json:"status"`
		OwnerAgent   domain.WorkflowAgent      `json:"owner_agent"`
		Dependencies []string                  `json:"dependencies,omitempty"`
		Outputs      []string                  `json:"outputs,omitempty"`
	}
	ctx := struct {
		WorkflowID     string              `json:"workflow_id"`
		State          string              `json:"state"`
		PlanVersion    int                 `json:"plan_version"`
		UserPrompt     string              `json:"user_prompt"`
		EnrichedPrompt string              `json:"enriched_prompt,omitempty"`
		CurrentTask    domain.WorkflowTask `json:"current_task"`
		RelevantTasks  []contextTask       `json:"relevant_tasks"`
	}{
		WorkflowID:     wf.ID,
		State:          string(wf.State),
		PlanVersion:    wf.PlanVersion,
		UserPrompt:     wf.UserPrompt,
		EnrichedPrompt: wf.EnrichedPrompt,
		CurrentTask:    task,
	}
	for _, item := range wf.Tasks {
		if item.ID == task.ID || containsTaskID(task.Dependencies, item.ID) || containsTaskID(item.Dependencies, task.ID) || item.Status == domain.WorkflowTaskDone {
			ctx.RelevantTasks = append(ctx.RelevantTasks, contextTask{
				ID:           item.ID,
				Title:        item.Title,
				Status:       item.Status,
				OwnerAgent:   item.OwnerAgent,
				Dependencies: append([]string(nil), item.Dependencies...),
				Outputs:      tailStrings(item.Outputs, 2),
			})
		}
	}
	data, _ := json.MarshalIndent(ctx, "", "  ")
	return string(data)
}

func containsTaskID(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func tailStrings(items []string, n int) []string {
	if n <= 0 || len(items) == 0 {
		return nil
	}
	if len(items) <= n {
		return append([]string(nil), items...)
	}
	return append([]string(nil), items[len(items)-n:]...)
}

func nextRunnableTask(wf domain.CoderWorkflow) (domain.WorkflowTask, bool) {
	for _, task := range wf.Tasks {
		if task.Status == domain.WorkflowTaskReady || task.Status == domain.WorkflowTaskPending {
			ready := true
			for _, dep := range task.Dependencies {
				if findTask(wf, dep).Status != domain.WorkflowTaskDone {
					ready = false
					break
				}
			}
			if ready {
				return task, true
			}
		}
	}
	return domain.WorkflowTask{}, false
}

func allDone(wf domain.CoderWorkflow) bool {
	if len(wf.Tasks) == 0 {
		return false
	}
	for _, task := range wf.Tasks {
		if task.Status != domain.WorkflowTaskDone && task.Status != domain.WorkflowTaskSkipped {
			return false
		}
	}
	return true
}

const orchestratorPrompt = `You are the orchestrator for apex-code coder mode.
Return strict JSON with keys:
- enriched_prompt: a concise but richer version of the user request
- planner_instructions: extra constraints or sequencing hints for the planner
The Go engine owns the workflow state machine and JSON file.
You only return content for this stage.
No markdown, no prose outside JSON.`

const plannerPrompt = `You are the planner for apex-code coder mode.
Return strict JSON with keys:
- summary: one short summary sentence
- phases: array of {name, description, task_ids}
- tasks: array of task objects

Each task object must include:
- id
- phase
- title
- description
- dependencies
- status
- owner_agent
- acceptance_criteria
- outputs

Allowed owner_agent values: orchestrator, planner, architecture, solutioner, tester, reviewer.
Allowed status values: pending.
The Go engine owns the authoritative workflow JSON and task lifecycle.
You only return the planner stage payload.
No markdown, no prose outside JSON.`

const architecturePrompt = `You are the architecture agent in apex-code coder mode.
Inspect the repository and answer in strict JSON with:
- status: done | blocked | needs_replan
- summary: concise explanation of architecture findings relevant to this task
- plan_mutations: optional array
Use tools when the task depends on real repository state.`

const solutionerPrompt = `You are the solutioner agent in apex-code coder mode.
Implement the task in the real workspace.
Answer in strict JSON with:
- status: done | blocked | needs_replan
- summary: concise implementation result
- plan_mutations: optional array
Use tools whenever edits, inspection, or verification require real state.`

const testerPrompt = `You are the testing agent in apex-code coder mode.
Run or select the right validations for the current task.
Answer in strict JSON with:
- status: done | blocked | needs_replan
- summary: concise test result summary
- plan_mutations: optional array
Use tools for commands and file inspection.`

const reviewerPrompt = `You are the reviewer agent in apex-code coder mode.
Review the produced work for bugs, regressions, and missing tests.
Answer in strict JSON with:
- status: done | blocked | needs_replan
- summary: concise review findings
- plan_mutations: optional array
Use tools when real repository state is needed.`

func executionPrompt(agentName domain.WorkflowAgent) string {
	switch agentName {
	case domain.WorkflowAgentArchitecture:
		return architecturePrompt
	case domain.WorkflowAgentTester:
		return testerPrompt
	case domain.WorkflowAgentReviewer:
		return reviewerPrompt
	default:
		return solutionerPrompt
	}
}

func emitProgress(onProgress func(ProgressEvent), event ProgressEvent) {
	if onProgress == nil {
		return
	}
	onProgress(event)
}

func latestTaskOutput(wf domain.CoderWorkflow, taskID string) string {
	idx := findTaskIndex(wf, taskID)
	if idx < 0 || len(wf.Tasks[idx].Outputs) == 0 {
		return ""
	}
	return strings.TrimSpace(wf.Tasks[idx].Outputs[len(wf.Tasks[idx].Outputs)-1])
}

func sumPromptTokens(state agent.LoopState) int {
	total := 0
	for _, turn := range state.Turns {
		total += turn.Response.Usage.PromptTokens
	}
	return total
}

func sumCompletionTokens(state agent.LoopState) int {
	total := 0
	for _, turn := range state.Turns {
		total += turn.Response.Usage.CompletionTokens
	}
	return total
}

func sumTotalTokens(state agent.LoopState) int {
	total := 0
	for _, turn := range state.Turns {
		total += turn.Response.Usage.TotalTokens
	}
	return total
}
