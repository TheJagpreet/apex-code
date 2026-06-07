package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/codermode"
	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/session"
	"github.com/apex-code/apex/internal/telemetry"
	"github.com/apex-code/apex/internal/tui"
)

// tuiAgent adapts Deps into the tui.Agent interface, preserving conversation
// context across REPL turns and projecting each run's budget/tool/diff state
// into the TUI's view types.
type tuiAgent struct {
	ctx      context.Context
	deps     *Deps
	messages []domain.Message
	seeded   bool
	ensured  bool
	mode     string
	workflow *domain.CoderWorkflow
}

func newTUIAgent(ctx context.Context, deps *Deps) *tuiAgent {
	agent := &tuiAgent{ctx: ctx, deps: deps, messages: cloneMessages(deps.Initial), seeded: len(deps.Initial) > 0, mode: "chat"}
	if deps.Coder != nil {
		if wf, ok, err := deps.Coder.LatestBySession(ctx, deps.SessionID); err == nil && ok {
			agent.workflow = &wf
			agent.mode = "coder"
		}
	}
	return agent
}

func (a *tuiAgent) Model() string { return a.deps.cfg.Model }

func (a *tuiAgent) CWD() string { return a.deps.cfg.CWD }

func (a *tuiAgent) SessionLabel() string {
	if a.deps.SessionID == "" {
		return "new"
	}
	return a.deps.SessionID
}

func (a *tuiAgent) LazyTools() bool { return a.deps.cfg.LazyTools }

func (a *tuiAgent) Extensions() tui.ExtensionView {
	if a.deps == nil {
		return tui.ExtensionView{}
	}
	return toExtensionView(a.deps.Extensions())
}

func (a *tuiAgent) ReloadExtensions(_ context.Context) (tui.ExtensionView, error) {
	if a.deps == nil {
		return tui.ExtensionView{}, nil
	}
	snapshot, err := a.deps.ReloadExtensions()
	if err != nil {
		return tui.ExtensionView{}, err
	}
	return toExtensionView(snapshot), nil
}

func (a *tuiAgent) SetActiveAgent(_ context.Context, name string) error {
	if a.deps == nil {
		return nil
	}
	return a.deps.SetActiveAgent(name)
}

func (a *tuiAgent) Mode() string {
	if strings.TrimSpace(a.mode) == "" {
		return "chat"
	}
	return a.mode
}

func (a *tuiAgent) SetMode(_ context.Context, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "chat":
		a.mode = "chat"
	case "coder":
		a.mode = "coder"
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	return nil
}

func (a *tuiAgent) CoderWorkflow() *domain.CoderWorkflow {
	if a.workflow == nil {
		return nil
	}
	wf := *a.workflow
	return &wf
}

func (a *tuiAgent) Send(ctx context.Context, prompt string) (tui.Reply, error) {
	return a.run(ctx, prompt, nil)
}

// Stream runs one turn while forwarding assistant text deltas to onDelta so the
// TUI can animate live output.
func (a *tuiAgent) Stream(ctx context.Context, prompt string, onDelta func(string)) (tui.Reply, error) {
	return a.run(ctx, prompt, onDelta)
}

func (a *tuiAgent) run(ctx context.Context, prompt string, onDelta func(string)) (tui.Reply, error) {
	if !a.ensured {
		if err := a.deps.EnsureModel(ctx); err != nil {
			return tui.Reply{}, err
		}
		a.ensured = true
	}
	if !a.seeded {
		if sys, ok := a.deps.SystemMessage(); ok {
			a.messages = append(a.messages, sys)
		}
		a.seeded = true
	}
	a.messages = append(a.messages, domain.Message{Role: domain.RoleUser, Content: prompt})

	var state agent.LoopState
	var err error
	if onDelta != nil {
		state, err = a.deps.RunConversationStream(ctx, a.messages, onDelta)
	} else {
		state, err = a.deps.RunConversation(ctx, a.messages)
	}
	if err != nil {
		return tui.Reply{}, err
	}
	// Carry the curated conversation forward for the next turn.
	a.messages = state.Messages

	reply := tui.Reply{
		Turns:       len(state.Turns),
		Termination: string(state.TerminationReason),
		Budget:      a.withSessionTokens(budgetSnapshot(state.LastBudget)),
		ToolCalls:   toolCallViews(state.Turns),
		Diffs:       diffViews(state.Turns),
		Mode:        a.mode,
		Workflow:    a.CoderWorkflow(),
	}
	if a.deps.Telemetry != nil {
		if totals, err := a.deps.Telemetry.Totals(ctx, a.deps.effectiveSessionID()); err == nil {
			reply.Stats = telemetry.FormatTotals(totals)
		}
	}
	if state.FinalResponse != nil {
		reply.Text = state.FinalResponse.Message.Content
	}
	return reply, nil
}

func (a *tuiAgent) ResumeSession(ctx context.Context, selector string) error {
	if err := a.deps.LoadResume(ctx, selector); err != nil {
		return err
	}
	a.messages = cloneMessages(a.deps.Initial)
	a.seeded = len(a.messages) > 0
	if a.deps.Coder != nil {
		if wf, ok, err := a.deps.Coder.LatestBySession(ctx, a.deps.SessionID); err == nil && ok {
			a.workflow = &wf
			a.mode = "coder"
		}
	}
	return nil
}

func (a *tuiAgent) NewSession() error {
	a.deps.SessionID = ""
	a.deps.Initial = nil
	a.messages = nil
	a.seeded = false
	a.workflow = nil
	a.mode = "chat"
	return nil
}

func (a *tuiAgent) SetModel(ctx context.Context, model string) error {
	nextCfg := a.deps.cfg
	nextCfg.Model = model
	client, err := newProvider(nextCfg)
	if err != nil {
		return err
	}
	if c, ok := client.(interface{ EnsureModel(context.Context) error }); ok {
		if err := c.EnsureModel(ctx); err != nil {
			return err
		}
	}
	a.deps.Provider = client
	a.deps.cfg = nextCfg
	a.deps.Context = contextmgr.New(client, contextmgr.Options{
		Logger: contextmgr.MultiInstrumenter{a.deps.Collector},
	})
	if a.deps.Coder != nil {
		a.deps.Coder = codermode.NewEngine(client, a.deps.Dispatcher, a.deps.Workflows, a.deps.Options)
	}
	return nil
}

func (a *tuiAgent) ListSessions(ctx context.Context, limit int) ([]tui.SessionOption, error) {
	if a.deps.Sessions == nil {
		return nil, nil
	}
	records, err := a.deps.Sessions.List(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]tui.SessionOption, 0, len(records))
	for _, record := range records {
		out = append(out, tui.SessionOption{
			ID:      record.ID,
			Model:   record.Model,
			Title:   record.Title,
			Summary: summarizeSession(record),
		})
	}
	return out, nil
}

func (a *tuiAgent) CoderSubmit(ctx context.Context, prompt string) (tui.Reply, error) {
	if a.deps.Coder == nil {
		return tui.Reply{}, fmt.Errorf("coder mode is unavailable")
	}
	wf, err := a.deps.Coder.CreatePlan(ctx, a.deps.effectiveSessionID(), ComposeCoderPrompt(prompt, a.messages))
	if err != nil {
		if latest, ok, loadErr := a.deps.Coder.LatestBySession(ctx, a.deps.effectiveSessionID()); loadErr == nil && ok {
			a.workflow = &latest
			a.mode = "coder"
			if a.deps.Workflows != nil {
				return tui.Reply{}, fmt.Errorf("%v\nWorkflow JSON: %s", err, a.deps.Workflows.WorkflowPath(latest))
			}
		}
		return tui.Reply{}, err
	}
	a.workflow = &wf
	a.mode = "coder"
	location := ""
	if a.deps.Workflows != nil {
		location = a.deps.Workflows.WorkflowPath(wf)
	}
	text := "Drafted a coder workflow plan. Review it in the plan pane, then use /approve, /replan, or /runplan."
	text = renderWorkflowChatSummary(wf, "Drafted a coder workflow plan. Review it below, then use `/approve` to approve and start execution, or `/replan <feedback>` to revise it.")
	if strings.TrimSpace(location) != "" {
		text += "\nWorkflow JSON: " + location
	}
	return tui.Reply{
		Text:     text,
		Budget:   a.withSessionTokens(a.coderBudget(ctx, wf)),
		Stats:    wfSummary(wf),
		Mode:     a.mode,
		Workflow: a.CoderWorkflow(),
	}, nil
}

func ComposeCoderPrompt(prompt string, history []domain.Message) string {
	prompt = sanitizeCoderPromptText(prompt)
	if prompt == "" {
		return ""
	}
	contextLines := coderContextLines(prompt, history)
	if len(contextLines) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString("Current request:\n")
	b.WriteString(prompt)
	b.WriteString("\n\nPrior conversation context from this session:\n")
	for _, line := range contextLines {
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func coderContextLines(prompt string, history []domain.Message) []string {
	prompt = sanitizeCoderPromptText(prompt)
	if len(history) == 0 {
		return nil
	}
	lines := make([]string, 0, 6)
	for i := len(history) - 1; i >= 0 && len(lines) < 6; i-- {
		msg := history[i]
		if msg.Role != domain.RoleUser && msg.Role != domain.RoleAssistant {
			continue
		}
		text := sanitizeCoderPromptText(msg.Content)
		if text == "" {
			continue
		}
		if msg.Role == domain.RoleUser && text == prompt {
			continue
		}
		label := "user"
		if msg.Role == domain.RoleAssistant {
			label = "assistant"
		}
		lines = append(lines, label+": "+compactCoderContext(text, 320))
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

func sanitizeCoderPromptText(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func compactCoderContext(text string, limit int) string {
	text = sanitizeCoderPromptText(text)
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func (a *tuiAgent) CoderReview(ctx context.Context, feedback string) (tui.Reply, error) {
	if a.workflow == nil {
		return tui.Reply{}, fmt.Errorf("no active coder workflow")
	}
	wf, err := a.deps.Coder.RevisePlan(ctx, *a.workflow, feedback)
	if err != nil {
		return tui.Reply{}, err
	}
	a.workflow = &wf
	return tui.Reply{
		Text:     renderWorkflowChatSummary(wf, "Planner revised the workflow. Updated plan below. Use `/approve` to approve and start execution, or `/replan <feedback>` to revise it again."),
		Budget:   a.withSessionTokens(a.coderBudget(ctx, wf)),
		Stats:    wfSummary(wf),
		Mode:     a.mode,
		Workflow: a.CoderWorkflow(),
	}, nil
}

func (a *tuiAgent) CoderApprove(ctx context.Context) (tui.Reply, error) {
	if a.workflow == nil {
		return tui.Reply{}, fmt.Errorf("no active coder workflow")
	}
	wf, err := a.deps.Coder.ApprovePlan(ctx, *a.workflow)
	if err != nil {
		return tui.Reply{}, err
	}
	a.workflow = &wf
	return tui.Reply{
		Text:     renderWorkflowChatSummary(wf, "Plan approved. Starting execution."),
		Budget:   a.withSessionTokens(a.coderBudget(ctx, wf)),
		Stats:    wfSummary(wf),
		Mode:     a.mode,
		Workflow: a.CoderWorkflow(),
	}, nil
}

func (a *tuiAgent) CoderExecute(ctx context.Context) (tui.Reply, error) {
	if a.workflow == nil {
		return tui.Reply{}, fmt.Errorf("no active coder workflow")
	}
	wf, err := a.deps.Coder.Execute(ctx, *a.workflow)
	a.workflow = &wf
	if err != nil {
		return tui.Reply{
			Text:     renderWorkflowExecutionSummary(wf, "Coder workflow execution stopped with an error: "+err.Error()),
			Budget:   a.withSessionTokens(a.coderBudget(ctx, wf)),
			Stats:    wfSummary(wf),
			Mode:     a.mode,
			Workflow: a.CoderWorkflow(),
		}, nil
	}
	reply := tui.Reply{
		Text:     renderWorkflowExecutionSummary(wf, "Coder workflow execution updated: "+string(wf.State)),
		Budget:   a.withSessionTokens(a.coderBudget(ctx, wf)),
		Mode:     a.mode,
		Workflow: a.CoderWorkflow(),
	}
	reply.Stats = wfSummary(wf)
	return reply, nil
}

func (a *tuiAgent) CoderExecuteStream(ctx context.Context, onUpdate func(tui.Reply)) (tui.Reply, error) {
	if a.workflow == nil {
		return tui.Reply{}, fmt.Errorf("no active coder workflow")
	}
	wf, err := a.deps.Coder.ExecuteStream(ctx, *a.workflow, func(event codermode.ProgressEvent) {
		a.workflow = &event.Workflow
		if onUpdate == nil {
			return
		}
		budget := a.coderBudget(ctx, event.Workflow)
		if event.Budget.PromptLimit > 0 || event.Budget.TotalPromptTokens > 0 {
			budget = budgetSnapshot(event.Budget)
		}
		onUpdate(tui.Reply{
			Text:     renderWorkflowProgressEvent(event),
			Budget:   a.withSessionTokens(budget),
			Stats:    wfSummary(event.Workflow),
			Mode:     a.mode,
			Workflow: a.CoderWorkflow(),
		})
	})
	a.workflow = &wf
	if err != nil {
		return tui.Reply{
			Text:     renderWorkflowExecutionSummary(wf, "Coder workflow execution stopped with an error: "+err.Error()),
			Budget:   a.withSessionTokens(a.coderBudget(ctx, wf)),
			Stats:    wfSummary(wf),
			Mode:     a.mode,
			Workflow: a.CoderWorkflow(),
		}, nil
	}
	return tui.Reply{
		Text:     renderWorkflowExecutionSummary(wf, "Coder workflow execution completed."),
		Budget:   a.withSessionTokens(a.coderBudget(ctx, wf)),
		Stats:    wfSummary(wf),
		Mode:     a.mode,
		Workflow: a.CoderWorkflow(),
	}, nil
}

func (a *tuiAgent) LiveStatus(ctx context.Context) (tui.Reply, error) {
	reply := tui.Reply{
		Mode:     a.mode,
		Workflow: a.CoderWorkflow(),
	}
	if a.workflow != nil {
		reply.Budget = a.withSessionTokens(a.coderBudget(ctx, *a.workflow))
		reply.Stats = wfSummary(*a.workflow)
	} else {
		reply.Budget = a.withSessionTokens(tui.BudgetSnapshot{})
	}
	if a.deps.Telemetry != nil {
		if totals, err := a.deps.Telemetry.Totals(ctx, a.deps.effectiveSessionID()); err == nil {
			reply.Stats = telemetry.FormatTotals(totals)
		}
	}
	return reply, nil
}

func wfSummary(wf domain.CoderWorkflow) string {
	done := 0
	for _, task := range wf.Tasks {
		if task.Status == domain.WorkflowTaskDone {
			done++
		}
	}
	return fmt.Sprintf("coder workflow  state=%s  progress=%d/%d  plan_version=%d", wf.State, done, len(wf.Tasks), wf.PlanVersion)
}

func renderWorkflowChatSummary(wf domain.CoderWorkflow, intro string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(intro))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("State: `%s`  Plan version: `%d`\n\n", wf.State, wf.PlanVersion))
	if len(wf.Tasks) > 0 {
		b.WriteString("## Plan\n\n")
		for i, task := range wf.Tasks {
			desc := strings.TrimSpace(task.Description)
			if desc == "" {
				desc = task.Title
			}
			b.WriteString(fmt.Sprintf("%d. `%s` -> %s\n", i+1, task.OwnerAgent, desc))
			if len(task.Dependencies) > 0 {
				b.WriteString(fmt.Sprintf("   depends on: %s\n", strings.Join(task.Dependencies, ", ")))
			}
			b.WriteString(fmt.Sprintf("   status: `%s`\n", task.Status))
		}
	}
	return strings.TrimSpace(b.String())
}

func renderWorkflowExecutionSummary(wf domain.CoderWorkflow, intro string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(intro))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("State: `%s`  Plan version: `%d`\n", wf.State, wf.PlanVersion))
	b.WriteString("\n")
	b.WriteString("## Session\n\n")
	b.WriteString(fmt.Sprintf("  session_id: %s\n", fallbackSession(wf.SessionID)))
	b.WriteString(fmt.Sprintf("  workflow_id: %s\n", wf.ID))
	b.WriteString("\n")
	b.WriteString("## Input\n\n")
	b.WriteString("  user:\n")
	b.WriteString(indentBlock(strings.TrimSpace(wf.UserPrompt), "    "))
	b.WriteString("\n")
	b.WriteString("\n")
	b.WriteString("## Tasks Performed\n\n")
	b.WriteString("  ")
	b.WriteString(agentRunChain(wf))
	b.WriteString("\n\n")
	b.WriteString("## Totals\n\n")
	b.WriteString(fmt.Sprintf("  cumulative_request_input_tokens: %d\n", workflowPromptTokenTotal(wf)))
	b.WriteString(fmt.Sprintf("  cumulative_request_output_tokens: %d\n", workflowCompletionTokenTotal(wf)))
	b.WriteString(fmt.Sprintf("  cumulative_request_total_tokens: %d\n", workflowTokenTotal(wf)))
	b.WriteString(fmt.Sprintf("  time_taken: %s\n", workflowDuration(wf)))
	return strings.TrimSpace(b.String())
}

func renderWorkflowProgressEvent(event codermode.ProgressEvent) string {
	switch event.Kind {
	case "started":
		if strings.TrimSpace(event.TaskTitle) != "" {
			return fmt.Sprintf("%s is running: %s", colorAgent(event.Agent), strings.TrimSpace(event.TaskTitle))
		}
		return fmt.Sprintf("%s is running", colorAgent(event.Agent))
	case "failed":
		if strings.TrimSpace(event.Error) != "" {
			return fmt.Sprintf("%s failed: %s", colorAgent(event.Agent), strings.TrimSpace(event.Error))
		}
		return fmt.Sprintf("%s failed", colorAgent(event.Agent))
	default:
		return ""
	}
}

func runSummary(run domain.WorkflowAgentRun) string {
	if strings.TrimSpace(run.Error) != "" {
		return "error: " + strings.TrimSpace(run.Error)
	}
	if strings.TrimSpace(run.Output) != "" {
		return compactLine(strings.TrimSpace(run.Output))
	}
	return ""
}

func workflowTokenTotal(wf domain.CoderWorkflow) int {
	total := 0
	for _, run := range wf.RunHistory {
		total += run.TotalTokens
	}
	return total
}

func workflowPromptTokenTotal(wf domain.CoderWorkflow) int {
	total := 0
	for _, run := range wf.RunHistory {
		total += run.PromptTokens
	}
	return total
}

func workflowCompletionTokenTotal(wf domain.CoderWorkflow) int {
	total := 0
	for _, run := range wf.RunHistory {
		total += run.CompletionTokens
	}
	return total
}

func workflowDuration(wf domain.CoderWorkflow) string {
	if wf.CreatedAt.IsZero() || wf.UpdatedAt.IsZero() || wf.UpdatedAt.Before(wf.CreatedAt) {
		return "n/a"
	}
	return wf.UpdatedAt.Sub(wf.CreatedAt).Round(time.Millisecond).String()
}

func colorAgent(agent domain.WorkflowAgent) string {
	switch agent {
	case domain.WorkflowAgentPlanner:
		return "`planner`"
	case domain.WorkflowAgentArchitecture:
		return "`architecture`"
	case domain.WorkflowAgentSolutioner:
		return "`solutioner`"
	case domain.WorkflowAgentTester:
		return "`tester`"
	case domain.WorkflowAgentReviewer:
		return "`reviewer`"
	default:
		return "`" + string(agent) + "`"
	}
}

func fallbackSession(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return "ad-hoc"
	}
	return strings.TrimSpace(sessionID)
}

func agentRunChain(wf domain.CoderWorkflow) string {
	if len(wf.RunHistory) == 0 {
		return "(no agent runs recorded yet)"
	}
	parts := make([]string, 0, len(wf.RunHistory))
	for _, run := range wf.RunHistory {
		label := string(run.Agent)
		if strings.Contains(strings.ToLower(run.Reason), "revise plan") {
			label += " (replan)"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " -> ")
}

func indentBlock(text string, indent string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return indent
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = indent + strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n")
}

func budgetSnapshot(r agent.BudgetReport) tui.BudgetSnapshot {
	names := make([]string, 0, len(r.TokensByPool))
	for name := range r.TokensByPool {
		names = append(names, string(name))
	}
	sort.Strings(names)

	pools := make([]tui.PoolSnapshot, 0, len(names))
	for _, name := range names {
		pn := agent.PoolName(name)
		pools = append(pools, tui.PoolSnapshot{
			Name:   name,
			Tokens: r.TokensByPool[pn],
			Limit:  r.PoolLimits[pn],
		})
	}
	return tui.BudgetSnapshot{
		PromptTokens:   r.TotalPromptTokens,
		PromptLimit:    r.PromptLimit,
		OutputHeadroom: r.OutputHeadroom,
		Pools:          pools,
	}
}

func (a *tuiAgent) withSessionTokens(b tui.BudgetSnapshot) tui.BudgetSnapshot {
	if a.deps != nil {
		b.SessionTokens = a.deps.currentSessionTokens()
		b.SessionTracked = true
	}
	return b
}

func (a *tuiAgent) coderBudget(ctx context.Context, wf domain.CoderWorkflow) tui.BudgetSnapshot {
	if a.deps == nil || a.deps.Provider == nil {
		return tui.BudgetSnapshot{}
	}
	caps := a.deps.Provider.Capabilities()
	if latest, ok := latestWorkflowBudget(wf); ok {
		return latest
	}
	totalTokens := 0
	if totalTokens == 0 {
		content := workflowBudgetText(wf)
		counted, err := a.deps.Provider.CountTokens(ctx, []domain.Message{{
			Role:    domain.RoleSystem,
			Content: content,
		}})
		if err == nil {
			totalTokens = counted
		}
	}
	return tui.BudgetSnapshot{
		PromptTokens:   totalTokens,
		PromptLimit:    caps.ContextWindow,
		OutputHeadroom: caps.MaxOutputTokens,
	}
}

func latestWorkflowBudget(wf domain.CoderWorkflow) (tui.BudgetSnapshot, bool) {
	for i := len(wf.RunHistory) - 1; i >= 0; i-- {
		run := wf.RunHistory[i]
		if run.BudgetPromptLimit <= 0 && run.BudgetPromptTokens <= 0 {
			continue
		}
		return tui.BudgetSnapshot{
			PromptTokens:   run.BudgetPromptTokens,
			PromptLimit:    run.BudgetPromptLimit,
			OutputHeadroom: run.BudgetOutputHeadroom,
		}, true
	}
	return tui.BudgetSnapshot{}, false
}

func workflowBudgetText(wf domain.CoderWorkflow) string {
	return codermode.WorkflowBudgetText(wf)
}

func toolCallViews(turns []agent.Turn) []tui.ToolCallView {
	var out []tui.ToolCallView
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			out = append(out, tui.ToolCallView{Name: c.Name, Args: string(c.Arguments)})
		}
	}
	return out
}

// diffViews extracts proposed edits from edit/write_file tool calls so the TUI
// diff viewer can show them (plan 9.6).
func diffViews(turns []agent.Turn) []tui.DiffView {
	var out []tui.DiffView
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			switch c.Name {
			case "edit", "write_file":
				out = append(out, extractDiff(c.Name, c.Arguments))
			}
		}
	}
	return out
}

func extractDiff(tool string, args json.RawMessage) tui.DiffView {
	var fields struct {
		Path    string `json:"path"`
		File    string `json:"file"`
		Patch   string `json:"patch"`
		Diff    string `json:"diff"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(args, &fields)
	file := fields.Path
	if file == "" {
		file = fields.File
	}
	patch := firstNonEmpty(fields.Patch, fields.Diff, fields.Content)
	return tui.DiffView{File: file, Patch: patch}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func summarizeSession(record session.Record) string {
	if record.Termination == "" {
		return record.Title
	}
	return record.Termination
}

func compactLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 120 {
		s = s[:120] + " …"
	}
	return s
}

func toExtensionView(snapshot ExtensionSnapshot) tui.ExtensionView {
	view := tui.ExtensionView{
		ActiveAgent:      snapshot.ActiveAgent,
		ActiveAgentFile:  snapshot.ActiveAgentFile,
		ActiveSkills:     append([]string(nil), snapshot.ActiveSkills...),
		ActiveSkillFiles: append([]string(nil), snapshot.ActiveSkillFiles...),
	}
	for _, agent := range snapshot.AvailableAgents {
		view.AvailableAgents = append(view.AvailableAgents, tui.BundleHeaderView{
			Name:        agent.Name,
			Description: agent.Description,
			File:        agent.File(),
		})
	}
	for _, skill := range snapshot.AvailableSkills {
		view.AvailableSkills = append(view.AvailableSkills, tui.BundleHeaderView{
			Name:        skill.Name,
			Description: skill.Description,
			File:        skill.File(),
		})
	}
	return view
}
