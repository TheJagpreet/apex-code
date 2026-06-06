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
	"github.com/apex-code/apex/internal/telemetry"
)

type ProgressEvent struct {
	Kind      string
	Agent     domain.WorkflowAgent
	TaskID    string
	TaskTitle string
	Summary   string
	Error     string
	Budget    agent.BudgetReport
	Workflow  domain.CoderWorkflow
}

type Engine struct {
	provider      provider.Provider
	tools         agent.ToolDispatcher
	store         *Store
	options       func() agent.Options
	telemetrySink func(context.Context, telemetry.SessionEvent) error
}

func NewEngine(p provider.Provider, tools agent.ToolDispatcher, store *Store, options func() agent.Options) *Engine {
	return &Engine{provider: p, tools: tools, store: store, options: options}
}

func (e *Engine) SetTelemetrySink(sink func(context.Context, telemetry.SessionEvent) error) {
	e.telemetrySink = sink
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
		updated, err := e.executeTask(ctx, wf, task, onProgress)
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
	raw, usage, stopReason, duration, err := e.runJSONTurn(ctx, []domain.Message{
		{Role: domain.RoleSystem, Content: orchestratorPrompt},
		{Role: domain.RoleUser, Content: wf.UserPrompt},
	}, &out)
	if err != nil {
		return "", "", raw, err
	}
	e.recordTelemetry(ctx, telemetry.SessionEvent{
		Mode:             "coder",
		Kind:             "stage_llm",
		Model:            "",
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CacheCreation:    usage.CacheCreationTokens,
		CacheRead:        usage.CacheReadTokens,
		DurationMs:       duration.Milliseconds(),
		Termination:      string(stopReason),
		WorkflowID:       wf.ID,
		Agent:            string(domain.WorkflowAgentOrchestrator),
		InputMessages:    cloneDomainMessages([]domain.Message{{Role: domain.RoleSystem, Content: orchestratorPrompt}, {Role: domain.RoleUser, Content: wf.UserPrompt}}),
		OutputMessage:    &domain.Message{Role: domain.RoleAssistant, Content: strings.TrimSpace(raw)},
	})
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
	raw, usage, stopReason, duration, err := e.runJSONTurn(ctx, []domain.Message{
		{Role: domain.RoleSystem, Content: plannerPrompt},
		{Role: domain.RoleUser, Content: input},
	}, &out)
	if err != nil {
		return domain.PlannerPlan{}, raw, input, err
	}
	e.recordTelemetry(ctx, telemetry.SessionEvent{
		Mode:             "coder",
		Kind:             "stage_llm",
		Model:            "",
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CacheCreation:    usage.CacheCreationTokens,
		CacheRead:        usage.CacheReadTokens,
		DurationMs:       duration.Milliseconds(),
		Termination:      string(stopReason),
		WorkflowID:       wf.ID,
		Agent:            string(domain.WorkflowAgentPlanner),
		InputMessages:    cloneDomainMessages([]domain.Message{{Role: domain.RoleSystem, Content: plannerPrompt}, {Role: domain.RoleUser, Content: input}}),
		OutputMessage:    &domain.Message{Role: domain.RoleAssistant, Content: strings.TrimSpace(raw)},
	})
	normalizePlannerPlan(&out)
	if err := validatePlannerPlan(out); err != nil {
		return domain.PlannerPlan{}, raw, input, err
	}
	for i := range out.Tasks {
		if out.Tasks[i].Status == "" {
			out.Tasks[i].Status = domain.WorkflowTaskPending
		}
	}
	return out, raw, input, nil
}

func (e *Engine) executeTask(ctx context.Context, wf domain.CoderWorkflow, task domain.WorkflowTask, onProgress func(ProgressEvent)) (domain.CoderWorkflow, error) {
	systemPrompt := executionPrompt(task.OwnerAgent)
	if systemPrompt == "" {
		systemPrompt = solutionerPrompt
	}
	opts := e.options()
	opts.MaxIterations = taskMaxIterations(task, opts.MaxIterations)
	opts.OnTurn = func(turn agent.Turn, budget agent.BudgetReport, iteration, maxIterations int) {
		output := cloneDomainMessages([]domain.Message{turn.Response.Message})
		var outputMessage *domain.Message
		if len(output) == 1 {
			outputMessage = &output[0]
		}
		e.recordTelemetry(ctx, telemetry.SessionEvent{
			Mode:             "coder",
			Kind:             "task_llm_turn",
			Model:            "",
			PromptTokens:     turn.Response.Usage.PromptTokens,
			CompletionTokens: turn.Response.Usage.CompletionTokens,
			TotalTokens:      turn.Response.Usage.TotalTokens,
			CacheCreation:    turn.Response.Usage.CacheCreationTokens,
			CacheRead:        turn.Response.Usage.CacheReadTokens,
			DurationMs:       turn.Duration.Milliseconds(),
			Termination:      string(turn.Response.StopReason),
			WorkflowID:       wf.ID,
			TaskID:           task.ID,
			Agent:            string(task.OwnerAgent),
			ToolCalls:        toolCallNames(turn.ToolCalls),
			ToolCallDetails:  cloneToolCalls(turn.ToolCalls),
			ToolResults:      len(turn.ToolResults),
			InputMessages:    cloneDomainMessages(turn.Request.Messages),
			OutputMessage:    outputMessage,
			Error:            errorString(turn.Err),
		})
		emitProgress(onProgress, ProgressEvent{
			Kind:      "turn",
			Agent:     task.OwnerAgent,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Budget:    budget,
			Workflow:  cloneWorkflow(wf),
		})
	}
	opts.OnToolResults = func(turn agent.Turn, iteration, maxIterations int) {
		for i, call := range turn.ToolCalls {
			var resultDetails []domain.ToolResult
			if i < len(turn.ToolResults) {
				resultDetails = cloneToolResults([]domain.ToolResult{turn.ToolResults[i]})
			}
			outcome, recoverable := telemetry.ToolExecOutcome(resultDetails)
			e.recordTelemetry(ctx, telemetry.SessionEvent{
				Mode:              "coder",
				Kind:              "tool_exec",
				Outcome:           outcome,
				Recoverable:       recoverable,
				Model:             "",
				DurationMs:        turn.ToolDuration.Milliseconds(),
				WorkflowID:        wf.ID,
				TaskID:            task.ID,
				Agent:             string(task.OwnerAgent),
				ToolCalls:         toolCallNames([]domain.ToolCall{call}),
				ToolCallDetails:   cloneToolCalls([]domain.ToolCall{call}),
				ToolResults:       len(resultDetails),
				ToolResultDetails: resultDetails,
			})
		}
	}
	messages := []domain.Message{
		{Role: domain.RoleSystem, Content: systemPrompt},
		{Role: domain.RoleSystem, Content: executionGuardPrompt},
		{Role: domain.RoleSystem, Content: "Workflow execution context:\n" + taskExecutionContext(wf, task)},
	}
	if directive := taskExecutionDirective(task); strings.TrimSpace(directive) != "" {
		messages = append(messages, domain.Message{Role: domain.RoleSystem, Content: directive})
	}
	messages = append(messages, domain.Message{
		Role:    domain.RoleUser,
		Content: fmt.Sprintf("Execute task %s: %s\n\nDescription:\n%s", task.ID, task.Title, task.Description),
	})
	startedAt := time.Now().UTC()
	state, err := agent.New(e.provider, e.tools).Run(ctx, messages, opts)
	if err != nil {
		return wf, err
	}
	state, err = e.finalizeExecutionState(ctx, task, systemPrompt, opts, state)
	if err != nil {
		return wf, err
	}
	completedAt := time.Now().UTC()
	if state.FinalResponse == nil {
		return wf, fmt.Errorf("task %s ended without a final response", task.ID)
	}
	raw := state.FinalResponse.Message.Content
	out := decodeExecutionDisposition(raw)
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
			"status":      out.Status,
		},
		PromptTokens:         sumPromptTokens(state),
		CompletionTokens:     sumCompletionTokens(state),
		TotalTokens:          sumTotalTokens(state),
		BudgetPromptTokens:   state.LastBudget.TotalPromptTokens,
		BudgetPromptLimit:    state.LastBudget.PromptLimit,
		BudgetOutputHeadroom: state.LastBudget.OutputHeadroom,
		DurationMs:           completedAt.Sub(startedAt).Milliseconds(),
		StartedAt:            startedAt,
		CompletedAt:          completedAt,
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

type executionDisposition struct {
	Status        string
	Summary       string
	PlanMutations []domain.WorkflowMutation
}

func (e *Engine) finalizeExecutionState(ctx context.Context, task domain.WorkflowTask, systemPrompt string, opts agent.Options, state agent.LoopState) (agent.LoopState, error) {
	if state.FinalResponse != nil || state.TerminationReason != agent.TerminationMaxIterations {
		return state, nil
	}
	finalizeMessages := cloneDomainMessages(state.Messages)
	finalizeInstruction := "Stop using tools now. Based only on the repository evidence already collected, return the final task result immediately as concise plain text. " +
		"Do not inspect more files. Do not ask questions. If you have enough evidence for a best-effort task result, give the best concise summary you can. " +
		"Prefix the response with 'BLOCKED:' only when a required external dependency, missing file, or tool failure truly prevents meaningful completion. " +
		"Prefix the response with 'NEEDS_REPLAN:' only when the current plan is no longer viable."
	if task.OwnerAgent == domain.WorkflowAgentSolutioner {
		finalizeInstruction += " If this task required creating, editing, or deleting files and that change did not already happen through successful tool calls, do not claim success; instead respond with 'BLOCKED:' and explain what did not happen."
	}
	finalizeMessages = append(finalizeMessages, domain.Message{
		Role:    domain.RoleUser,
		Content: finalizeInstruction,
	})
	finalizeOpts := opts
	finalizeOpts.Tools = nil
	finalizeOpts.ToolProvider = nil
	finalizeOpts.MaxIterations = 2
	finalizeOpts.StreamText = nil
	finalizeState, err := agent.New(e.provider, nil).Run(ctx, finalizeMessages, finalizeOpts)
	if err != nil {
		return state, err
	}
	if finalizeState.FinalResponse == nil {
		return state, fmt.Errorf("task %s ended without a final response after forced finalization (reason=%s)", task.ID, finalizeState.TerminationReason)
	}
	state.Iteration += finalizeState.Iteration
	state.Messages = finalizeState.Messages
	state.Turns = append(state.Turns, finalizeState.Turns...)
	state.FinalResponse = finalizeState.FinalResponse
	state.TerminationReason = finalizeState.TerminationReason
	state.LastError = finalizeState.LastError
	state.LastBudget = finalizeState.LastBudget
	state.Phase = finalizeState.Phase
	return state, nil
}

func decodeExecutionDisposition(raw string) executionDisposition {
	var parsed executionOutput
	if err := decodeJSONObject(raw, &parsed); err == nil {
		status := strings.ToLower(strings.TrimSpace(parsed.Status))
		if status == "" {
			status = "done"
		}
		summary := strings.TrimSpace(parsed.Summary)
		if summary == "" {
			summary = strings.TrimSpace(raw)
		}
		return executionDisposition{
			Status:        status,
			Summary:       summary,
			PlanMutations: parsed.PlanMutations,
		}
	}
	text := strings.TrimSpace(raw)
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, "blocked:"):
		return executionDisposition{
			Status:  "blocked",
			Summary: strings.TrimSpace(text[len("blocked:"):]),
		}
	case strings.HasPrefix(lower, "needs_replan:"):
		return executionDisposition{
			Status:  "needs_replan",
			Summary: strings.TrimSpace(text[len("needs_replan:"):]),
		}
	case strings.HasPrefix(lower, "replan:"):
		return executionDisposition{
			Status:  "needs_replan",
			Summary: strings.TrimSpace(text[len("replan:"):]),
		}
	default:
		return executionDisposition{
			Status:  "done",
			Summary: text,
		}
	}
}

func cloneDomainMessages(messages []domain.Message) []domain.Message {
	out := make([]domain.Message, 0, len(messages))
	for _, msg := range messages {
		copied := domain.Message{
			Role:         msg.Role,
			Content:      msg.Content,
			CacheControl: msg.CacheControl,
		}
		copied.ToolCalls = cloneToolCalls(msg.ToolCalls)
		copied.ToolResults = cloneToolResults(msg.ToolResults)
		out = append(out, copied)
	}
	return out
}

func cloneToolCalls(calls []domain.ToolCall) []domain.ToolCall {
	out := make([]domain.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, domain.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: append([]byte(nil), call.Arguments...),
		})
	}
	return out
}

func cloneToolResults(results []domain.ToolResult) []domain.ToolResult {
	out := make([]domain.ToolResult, 0, len(results))
	for _, result := range results {
		out = append(out, result)
	}
	return out
}

func (e *Engine) runJSONTurn(ctx context.Context, messages []domain.Message, target any) (string, domain.Usage, domain.StopReason, time.Duration, error) {
	started := time.Now()
	stream, err := e.provider.Complete(ctx, domain.Request{
		Model:    "",
		Messages: messages,
	})
	if err != nil {
		return "", domain.Usage{}, domain.StopUnknown, 0, err
	}
	defer stream.Close()
	var text strings.Builder
	var usage domain.Usage
	stopReason := domain.StopUnknown
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", domain.Usage{}, domain.StopUnknown, time.Since(started), err
		}
		if ev.Kind == provider.EventText {
			text.WriteString(ev.Text)
		}
		if ev.Kind == provider.EventDone {
			stopReason = ev.StopReason
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		}
	}
	raw := strings.TrimSpace(text.String())
	if err := decodeJSONObject(raw, target); err != nil {
		return raw, usage, stopReason, time.Since(started), err
	}
	return raw, usage, stopReason, time.Since(started), nil
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
		plan.Tasks[i].OwnerAgent = normalizeTaskOwner(plan.Tasks[i])
		for j := range plan.Tasks[i].Dependencies {
			plan.Tasks[i].Dependencies[j] = strings.TrimSpace(plan.Tasks[i].Dependencies[j])
		}
	}
}

func validatePlannerPlan(plan domain.PlannerPlan) error {
	if len(plan.Tasks) == 0 {
		return fmt.Errorf("planner returned no tasks")
	}
	for _, task := range plan.Tasks {
		if strings.TrimSpace(task.ID) == "" {
			return fmt.Errorf("planner task is missing id")
		}
		if strings.TrimSpace(task.Title) == "" {
			return fmt.Errorf("planner task %q is missing title", task.ID)
		}
		if strings.TrimSpace(task.Description) == "" {
			return fmt.Errorf("planner task %q is missing description", task.ID)
		}
	}
	return nil
}

func normalizeTaskOwner(task domain.WorkflowTask) domain.WorkflowAgent {
	switch task.OwnerAgent {
	case domain.WorkflowAgentArchitecture, domain.WorkflowAgentSolutioner, domain.WorkflowAgentTester, domain.WorkflowAgentReviewer:
		return task.OwnerAgent
	}
	text := strings.ToLower(strings.TrimSpace(task.Title + "\n" + task.Description))
	switch {
	case strings.Contains(text, "review"), strings.Contains(text, "regression"), strings.Contains(text, "bug risk"):
		return domain.WorkflowAgentReviewer
	case strings.Contains(text, "test"), strings.Contains(text, "verify"), strings.Contains(text, "validation"), strings.Contains(text, "assert"):
		return domain.WorkflowAgentTester
	case strings.Contains(text, "architecture"), strings.Contains(text, "design"), strings.Contains(text, "data flow"), strings.Contains(text, "codebase"):
		return domain.WorkflowAgentArchitecture
	default:
		return domain.WorkflowAgentSolutioner
	}
}

func workflowJSON(wf domain.CoderWorkflow) string {
	data, _ := json.MarshalIndent(wf, "", "  ")
	return string(data)
}

func taskExecutionContext(wf domain.CoderWorkflow, task domain.WorkflowTask) string {
	type handoffTask struct {
		ID         string               `json:"id"`
		Title      string               `json:"title"`
		OwnerAgent domain.WorkflowAgent `json:"owner_agent"`
		Outputs    []string             `json:"outputs,omitempty"`
	}
	type currentTask struct {
		ID                 string               `json:"id"`
		Title              string               `json:"title"`
		Description        string               `json:"description"`
		OwnerAgent         domain.WorkflowAgent `json:"owner_agent"`
		AcceptanceCriteria []string             `json:"acceptance_criteria,omitempty"`
		ExpectedOutputs    []string             `json:"expected_outputs,omitempty"`
	}
	ctx := struct {
		WorkflowID         string        `json:"workflow_id"`
		PlanVersion        int           `json:"plan_version"`
		OriginalRequest    string        `json:"original_request"`
		CurrentTask        currentTask   `json:"current_task"`
		DependencyHandoffs []handoffTask `json:"dependency_handoffs,omitempty"`
	}{
		WorkflowID:      wf.ID,
		PlanVersion:     wf.PlanVersion,
		OriginalRequest: wf.UserPrompt,
		CurrentTask: currentTask{
			ID:                 task.ID,
			Title:              task.Title,
			Description:        task.Description,
			OwnerAgent:         task.OwnerAgent,
			AcceptanceCriteria: append([]string(nil), task.AcceptanceCriteria...),
			ExpectedOutputs:    append([]string(nil), task.Outputs...),
		},
	}
	for _, depID := range task.Dependencies {
		item := findTask(wf, depID)
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		ctx.DependencyHandoffs = append(ctx.DependencyHandoffs, handoffTask{
			ID:         item.ID,
			Title:      item.Title,
			OwnerAgent: item.OwnerAgent,
			Outputs:    compactHandoffOutputs(item.Outputs),
		})
	}
	data, _ := json.MarshalIndent(ctx, "", "  ")
	return string(data)
}

func compactHandoffOutputs(outputs []string) []string {
	items := tailStrings(outputs, 1)
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if compact := compactSummary(item); strings.TrimSpace(compact) != "" {
			out = append(out, compact)
		}
	}
	return out
}

func taskExecutionDirective(task domain.WorkflowTask) string {
	if taskRequiresWorkspaceMutation(task) {
		return ""
	}
	return "This is a non-mutating task handoff. Prefer using dependency handoffs and evidence already collected by prior tasks. " +
		"Do not create or modify files unless the task description explicitly requires it. " +
		"Do not reopen the repository broadly if the handoff already provides enough information to answer."
}

func taskMaxIterations(task domain.WorkflowTask, current int) int {
	if current <= 0 {
		current = 50
	}
	if taskRequiresWorkspaceMutation(task) {
		return current
	}
	limit := 8
	if task.OwnerAgent == domain.WorkflowAgentArchitecture || task.OwnerAgent == domain.WorkflowAgentReviewer {
		limit = 16
	}
	if current > limit {
		return limit
	}
	return current
}

func taskRequiresWorkspaceMutation(task domain.WorkflowTask) bool {
	text := strings.ToLower(strings.TrimSpace(task.Title + "\n" + task.Description + "\n" + strings.Join(task.AcceptanceCriteria, "\n") + "\n" + strings.Join(task.Outputs, "\n")))
	for _, marker := range []string{
		"create file", "write file", "write ", "edit ", "modify ", "update ", "delete ",
		"overwrite ", "patch ", "run the script", "execute ", "save ", "generate file",
		"exists at", "file exists", ".md file", ".go file", ".txt file", ".json file", ".toml file", ".yml file", ".yaml file",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if strings.Contains(text, "transient, internal") || strings.Contains(text, "design notes") || strings.Contains(text, "flow map notes") {
		return false
	}
	return false
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

Allowed owner_agent values: architecture, solutioner, tester, reviewer.
Allowed status values: pending.
The Go engine owns the authoritative workflow JSON and task lifecycle.
You only return the planner stage payload.
Never assign execution tasks to orchestrator or planner. Those agents are stage-only and do not execute workflow tasks.
No markdown, no prose outside JSON.`

const architecturePrompt = `You are the architecture agent in apex-code coder mode.
Inspect the repository and answer with a concise plain-text task result.
Once you have enough evidence to satisfy the task, stop and return the result immediately.
Do not keep exploring after the acceptance criteria are satisfied.
Avoid repeating the same tool call for the same file or line range unless a previous tool result failed.
For analysis tasks, prefer a best-effort status=done summary once you can explain the main flow. Use blocked only for real external blockers.
If the task is truly blocked, prefix the response with 'BLOCKED:'.
If the task needs replanning, prefix the response with 'NEEDS_REPLAN:'.
Use tools when the task depends on real repository state.`

const executionGuardPrompt = `You are operating on a real workspace.
Use tools for repository inspection, file edits, and command execution whenever the task depends on real workspace state.
Never claim you changed a file, ran a command, or verified a fix unless the corresponding tool actually succeeded.
For file edits, prefer read_file before edit or write_file, and summarize the actual tool outcomes.`

const solutionerPrompt = `You are the solutioner agent in apex-code coder mode.
Implement the task in the real workspace.
Answer with a concise plain-text task result that reflects the real workspace outcome.
Once the task is completed or concretely blocked, stop and return the result immediately.
Avoid repeating the same tool call for the same file or line range unless a previous tool result failed.
If the task is truly blocked, prefix the response with 'BLOCKED:'.
If the task needs replanning, prefix the response with 'NEEDS_REPLAN:'.
Use tools whenever edits, inspection, or verification require real state.`

const testerPrompt = `You are the testing agent in apex-code coder mode.
Run or select the right validations for the current task.
Answer with a concise plain-text task result summarizing the validation outcome.
Once you have enough validation evidence, stop and return the result immediately.
Avoid repeating the same tool call for the same file or line range unless a previous tool result failed.
Prefer status=done when you can report the best available validation result. Use blocked only for real external blockers.
If the task is truly blocked, prefix the response with 'BLOCKED:'.
If the task needs replanning, prefix the response with 'NEEDS_REPLAN:'.
Use tools for commands and file inspection.`

const reviewerPrompt = `You are the reviewer agent in apex-code coder mode.
Review the produced work for bugs, regressions, and missing tests.
Answer with a concise plain-text task result summarizing the review findings.
Once you have enough review evidence, stop and return the result immediately.
Avoid repeating the same tool call for the same file or line range unless a previous tool result failed.
Prefer status=done when you can deliver the best available review summary. Use blocked only for real external blockers.
If the task is truly blocked, prefix the response with 'BLOCKED:'.
If the task needs replanning, prefix the response with 'NEEDS_REPLAN:'.
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

func (e *Engine) recordTelemetry(ctx context.Context, event telemetry.SessionEvent) {
	if e.telemetrySink == nil {
		return
	}
	_ = e.telemetrySink(ctx, event)
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

func toolCallNames(calls []domain.ToolCall) []string {
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.Name) != "" {
			out = append(out, strings.TrimSpace(call.Name))
		}
	}
	return out
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
