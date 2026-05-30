package cli

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider/ollama"
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
}

func newTUIAgent(ctx context.Context, deps *Deps) *tuiAgent {
	return &tuiAgent{ctx: ctx, deps: deps, messages: cloneMessages(deps.Initial), seeded: len(deps.Initial) > 0}
}

func (a *tuiAgent) Model() string { return a.deps.cfg.Model }

func (a *tuiAgent) CWD() string { return a.deps.cfg.CWD }

func (a *tuiAgent) SessionLabel() string {
	if a.deps.SessionID == "" {
		return "new"
	}
	if a.deps.Sessions == nil {
		return a.deps.SessionID
	}
	record, _, _, err := a.deps.Sessions.Load(a.ctx, a.deps.SessionID)
	if err != nil {
		return a.deps.SessionID
	}
	if record.Title != "" {
		return record.Title
	}
	return record.ID
}

func (a *tuiAgent) LazyTools() bool { return a.deps.cfg.LazyTools }

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
		Budget:      budgetSnapshot(state.LastBudget),
		ToolCalls:   toolCallViews(state.Turns),
		Diffs:       diffViews(state.Turns),
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
	return nil
}

func (a *tuiAgent) NewSession() error {
	a.deps.SessionID = ""
	a.deps.Initial = nil
	a.messages = nil
	a.seeded = false
	return nil
}

func (a *tuiAgent) SetModel(ctx context.Context, model string) error {
	client := ollama.New(ollama.WithModel(model), ollama.WithBaseURL(a.deps.cfg.BaseURL))
	if err := client.EnsureModel(ctx); err != nil {
		return err
	}
	a.deps.Provider = client
	a.deps.cfg.Model = model
	a.deps.Context = contextmgr.New(client, contextmgr.Options{
		Logger: contextmgr.MultiInstrumenter{a.deps.Collector},
	})
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
