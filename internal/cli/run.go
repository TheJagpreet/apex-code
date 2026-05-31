// Package cli wires apex-code's invocation modes (one-shot, pipe, interactive
// TUI) on top of the agent loop, provider layer, tool registry, and the
// dynamic tool/skill loading from Phase 8.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/codermode"
	"github.com/apex-code/apex/internal/config"
	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/mcp"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/ollama"
	"github.com/apex-code/apex/internal/session"
	"github.com/apex-code/apex/internal/skills"
	"github.com/apex-code/apex/internal/telemetry"
	"github.com/apex-code/apex/internal/tools"
)

// Config captures the resolved flags/inputs for a single apex invocation.
type Config struct {
	Model         string
	BaseURL       string
	MaxIterations int
	LazyTools     bool
	SkillRoots    []string
	Budget        agent.BudgetFractions
	Prompt        string
	CWD           string
	StateDBPath   string
	WorkflowRoot  string
	Resume        string
	Features      config.Features
	MCPServers    []config.MCPServer
}

// Deps bundles the long-lived collaborators an agent run needs. Building them
// once lets the one-shot, pipe, and interactive modes share identical wiring.
type Deps struct {
	Provider   provider.Provider
	Registry   *tools.Registry
	Dispatcher *tools.Dispatcher
	Context    *contextmgr.Manager
	Router     *tools.Router
	Lazy       *tools.LazySet
	Skills     *skills.Loader
	Sessions   *session.Store
	Telemetry  *telemetry.Store
	Collector  *telemetry.Collector
	Workflows  *codermode.Store
	Coder      *codermode.Engine
	SessionID  string
	Initial    []domain.Message
	cfg        Config
}

// BuildDeps assembles the provider, tool registry, context manager, and the
// lazy tool/skill machinery for the given config.
func BuildDeps(cfg Config) (*Deps, error) {
	client := ollama.New(ollama.WithModel(cfg.Model), ollama.WithBaseURL(cfg.BaseURL))
	registry := tools.NewDefaultRegistry()
	router := tools.NewRouter(registry)

	loader := skills.NewLoader(cfg.SkillRoots...)
	_ = loader.Discover()

	collector := &telemetry.Collector{}
	ctxMgr := contextmgr.New(client, contextmgr.Options{
		Logger: contextmgr.MultiInstrumenter{collector},
	})

	deps := &Deps{
		Provider:   client,
		Registry:   registry,
		Dispatcher: tools.NewDispatcher(registry),
		Context:    ctxMgr,
		Router:     router,
		Lazy:       tools.NewLazySet(router),
		Skills:     loader,
		Collector:  collector,
		cfg:        cfg,
	}

	if err := os.MkdirAll(filepathDir(cfg.StateDBPath), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	if cfg.Features.Sessions {
		store, err := session.Open(cfg.StateDBPath)
		if err != nil {
			return nil, fmt.Errorf("open session store: %w", err)
		}
		deps.Sessions = store
	}
	if cfg.Features.Telemetry {
		store, err := telemetry.Open(cfg.StateDBPath)
		if err != nil {
			return nil, fmt.Errorf("open telemetry store: %w", err)
		}
		deps.Telemetry = store
	}
	workflowRoot := cfg.WorkflowRoot
	if strings.TrimSpace(workflowRoot) == "" {
		workflowRoot = filepath.Join(filepathDir(cfg.StateDBPath), "workflows")
	}
	wfStore, err := codermode.OpenStore(workflowRoot)
	if err != nil {
		return nil, fmt.Errorf("open workflow store: %w", err)
	}
	deps.Workflows = wfStore
	deps.Coder = codermode.NewEngine(client, deps.Dispatcher, wfStore, deps.Options)
	if cfg.Features.MCP {
		for _, server := range cfg.MCPServers {
			if !server.Enabled || strings.TrimSpace(server.Command) == "" {
				continue
			}
			client, err := mcp.NewClient(mcp.Config{
				Name:    server.Name,
				Command: server.Command,
				Args:    server.Args,
				Env:     server.Env,
			})
			if err != nil {
				return nil, err
			}
			wrapped, err := tools.WrapMCPClient(client, tools.NewGate(tools.DefaultGateOptions()))
			if err != nil {
				return nil, err
			}
			for _, tool := range wrapped {
				if err := deps.Registry.Register(tool); err != nil {
					return nil, err
				}
			}
		}
	}
	return deps, nil
}

func (d *Deps) Close() error {
	var first error
	if d.Sessions != nil {
		if err := d.Sessions.Close(); err != nil && first == nil {
			first = err
		}
	}
	if d.Telemetry != nil {
		if err := d.Telemetry.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// EnsureModel verifies the configured model is available, surfacing the
// actionable "ollama pull" error if not.
func (d *Deps) EnsureModel(ctx context.Context) error {
	if c, ok := d.Provider.(*ollama.Client); ok {
		return c.EnsureModel(ctx)
	}
	return nil
}

// systemMessage builds the run's system message. In lazy mode it advertises the
// deferred tool catalogue and any discovered skills so the model knows what it
// can ask for without paying for full schemas (plan 8.1/8.5).
func (d *Deps) systemMessage() (domain.Message, bool) {
	var b strings.Builder
	b.WriteString("You are apex-code, a token-efficient coding agent.\n")
	b.WriteString("Use tools for repository inspection, file edits, and command execution whenever the task depends on real workspace state.\n")
	b.WriteString("Never claim you changed a file, ran a command, or verified a fix unless the corresponding tool actually succeeded.\n")
	b.WriteString("For file edits, prefer read_file before edit/write_file, and summarize the real outcome of the tool calls.\n")
	if d.cfg.LazyTools {
		b.WriteString("\nAvailable tools (ask for one by name to use it):\n")
		b.WriteString(d.Registry.DescribeDeferred())
		if skillsDoc := d.Skills.Describe(); skillsDoc != "" {
			b.WriteString("\n\nAvailable skills:\n")
			b.WriteString(skillsDoc)
		}
	}
	return domain.Message{Role: domain.RoleSystem, Content: b.String()}, true
}

// Options builds the agent options for this run, including dynamic tool
// injection when lazy mode is enabled.
func (d *Deps) Options() agent.Options {
	opts := agent.Options{
		Model:           d.cfg.Model,
		MaxIterations:   d.cfg.MaxIterations,
		BudgetFractions: d.cfg.Budget,
		Compactor:       d.Context.Compactor(),
		KeepAlive:       "10m",
	}

	if d.cfg.LazyTools {
		// Seed nothing: schemas are paid for a turn at a time as the model (or
		// the user prompt) names tools/skills (plan 8.2/8.3).
		opts.ToolProvider = func(messages []domain.Message) []domain.ToolSpec {
			for _, msg := range messages {
				if msg.Role == domain.RoleUser || msg.Role == domain.RoleAssistant {
					d.Lazy.InjectFromText(msg.Content)
				}
				for _, call := range msg.ToolCalls {
					d.Lazy.Inject(call.Name)
				}
			}
			for _, name := range d.Skills.Match(lastUserText(messages)) {
				_, _ = d.Skills.Activate(name, d.Lazy)
			}
			return d.Lazy.Specs()
		}
	} else {
		opts.Tools = d.Registry.Specs()
	}
	return opts
}

// RunOnce executes the agent loop for a single prompt and returns the final
// loop state.
func (d *Deps) RunOnce(ctx context.Context, prompt string) (agent.LoopState, error) {
	messages := cloneMessages(d.Initial)
	if sys, ok := d.systemMessage(); ok {
		if len(messages) == 0 {
			messages = append(messages, sys)
		}
	}
	if looksLikeWorkspaceEdit(strings.ToLower(prompt)) {
		messages = append(messages, domain.Message{
			Role: domain.RoleSystem,
			Content: "This is a real workspace editing task. Use tools immediately. " +
				"Do not ask the user for confirmation before reading, editing, writing, or verifying files. " +
				"Only finish once the change is actually applied or a concrete tool error blocks you.",
		})
	}
	messages = append(messages, domain.Message{Role: domain.RoleUser, Content: prompt})
	return d.RunConversation(ctx, messages)
}

// RunConversation runs the agent loop over an existing message slice, letting
// the interactive REPL preserve context across turns.
func (d *Deps) RunConversation(ctx context.Context, messages []domain.Message) (agent.LoopState, error) {
	return d.runConversation(ctx, messages, nil)
}

// RunConversationStream behaves like RunConversation but forwards each assistant
// text delta to onText as it streams in, enabling live TUI output.
func (d *Deps) RunConversationStream(ctx context.Context, messages []domain.Message, onText func(string)) (agent.LoopState, error) {
	return d.runConversation(ctx, messages, onText)
}

func (d *Deps) runConversation(ctx context.Context, messages []domain.Message, onText func(string)) (agent.LoopState, error) {
	opts := d.Options()
	opts.StreamText = onText
	runMessages := cloneMessages(messages)
	state, err := agent.New(d.Provider, d.Dispatcher).Run(ctx, runMessages, opts)
	if err == nil && shouldRetryWithToolNudge(runMessages, state) {
		nudged := cloneMessages(messages)
		nudged = append(nudged, domain.Message{
			Role: domain.RoleSystem,
			Content: "This task requires real workspace action. Use the available tools now. " +
				"Do not answer in prose unless a tool actually succeeded or returned a concrete error.",
		})
		runMessages = nudged
		state, err = agent.New(d.Provider, d.Dispatcher).Run(ctx, nudged, opts)
	}
	if persistErr := d.persistRun(ctx, runMessages, state); persistErr != nil && err == nil {
		// Persistence failures should not mask a successful tool/model run.
		// Session/telemetry durability is best-effort at the UX boundary.
		state.LastError = persistErr
	}
	return state, err
}

// SystemMessage exposes the run's system message (if any) so callers seeding
// their own conversation can include it.
func (d *Deps) SystemMessage() (domain.Message, bool) {
	return d.systemMessage()
}

func lastUserText(messages []domain.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == domain.RoleUser {
			return messages[i].Content
		}
	}
	return ""
}

func (d *Deps) LoadResume(ctx context.Context, selector string) error {
	if d.Sessions == nil {
		return fmt.Errorf("sessions are disabled")
	}
	record, snap, _, err := d.Sessions.Load(ctx, selector)
	if err != nil {
		return err
	}
	d.SessionID = record.ID
	rehydrated := d.Context.Rehydrate(snap.WorkingSet)
	d.Initial = d.Context.Messages(rehydrated)
	return nil
}

func (d *Deps) persistRun(ctx context.Context, inputMessages []domain.Message, state agent.LoopState) error {
	savedBy := d.Collector.LastSavedBy()
	if d.cfg.LazyTools {
		lazy := tools.MeasureSchemaTokens(nil, d.Registry, d.Lazy.Active()).Saved()
		if lazy > 0 {
			if savedBy == nil {
				savedBy = map[string]int{}
			}
			savedBy["lazy_tools"] += lazy
		}
	}
	if d.Sessions != nil {
		snapshot := session.Snapshot{
			Version:    1,
			Model:      d.cfg.Model,
			CWD:        d.cfg.CWD,
			WorkingSet: d.Context.FromMessages(state.Messages),
		}
		record, _, err := d.Sessions.Save(ctx, session.SaveInput{
			SessionID:   d.SessionID,
			Model:       d.cfg.Model,
			CWD:         d.cfg.CWD,
			Prompt:      lastUserText(inputMessages),
			Termination: string(state.TerminationReason),
			Snapshot:    snapshot,
			Turns:       turnRecords(state, savedBy),
		})
		if err != nil {
			return err
		}
		d.SessionID = record.ID
	}
	if d.Telemetry != nil {
		for i, turn := range state.Turns {
			if err := d.Telemetry.SaveTurn(ctx, telemetry.TurnMetric{
				SessionID:        d.effectiveSessionID(),
				TurnIndex:        i + 1,
				Model:            d.cfg.Model,
				PromptTokens:     turn.Response.Usage.PromptTokens,
				CompletionTokens: turn.Response.Usage.CompletionTokens,
				TotalTokens:      turn.Response.Usage.TotalTokens,
				CacheCreation:    turn.Response.Usage.CacheCreationTokens,
				CacheRead:        turn.Response.Usage.CacheReadTokens,
				DurationMs:       turn.Duration.Milliseconds(),
				Termination:      string(state.TerminationReason),
				SavedBy:          savedBy,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Deps) effectiveSessionID() string {
	if d.SessionID != "" {
		return d.SessionID
	}
	return "ad-hoc"
}

func turnRecords(state agent.LoopState, savedBy map[string]int) []session.TurnRecord {
	out := make([]session.TurnRecord, 0, len(state.Turns))
	for i, turn := range state.Turns {
		rec := session.TurnRecord{
			Index:            i + 1,
			PromptTokens:     turn.Response.Usage.PromptTokens,
			CompletionTokens: turn.Response.Usage.CompletionTokens,
			TotalTokens:      turn.Response.Usage.TotalTokens,
			StopReason:       string(turn.Response.StopReason),
			ToolCalls:        len(turn.ToolCalls),
			ToolResults:      len(turn.ToolResults),
			CacheCreation:    turn.Response.Usage.CacheCreationTokens,
			CacheRead:        turn.Response.Usage.CacheReadTokens,
			SavedBy:          cloneSavedBy(savedBy),
		}
		if turn.Err != nil {
			rec.Error = turn.Err.Error()
		}
		out = append(out, rec)
	}
	return out
}

func cloneSavedBy(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func filepathDir(path string) string {
	if strings.TrimSpace(path) == "" {
		return "."
	}
	return filepath.Dir(path)
}

func cloneMessages(messages []domain.Message) []domain.Message {
	out := make([]domain.Message, 0, len(messages))
	for _, m := range messages {
		out = append(out, domain.Message{
			Role:         m.Role,
			Content:      m.Content,
			ToolCalls:    append([]domain.ToolCall(nil), m.ToolCalls...),
			ToolResults:  append([]domain.ToolResult(nil), m.ToolResults...),
			CacheControl: m.CacheControl,
		})
	}
	return out
}

func shouldRetryWithToolNudge(messages []domain.Message, state agent.LoopState) bool {
	if state.FinalResponse == nil || len(state.Turns) == 0 {
		return false
	}
	if totalToolCalls(state) > 0 {
		return false
	}
	prompt := strings.ToLower(lastUserText(messages))
	if !looksLikeWorkspaceEdit(prompt) {
		return false
	}
	return true
}

func totalToolCalls(state agent.LoopState) int {
	total := 0
	for _, turn := range state.Turns {
		total += len(turn.ToolCalls)
	}
	return total
}

func looksLikeWorkspaceEdit(prompt string) bool {
	if prompt == "" {
		return false
	}
	hasEditVerb := false
	for _, verb := range []string{"update", "edit", "change", "modify", "fix", "rewrite", "write", "set"} {
		if strings.Contains(prompt, verb) {
			hasEditVerb = true
			break
		}
	}
	if !hasEditVerb {
		return false
	}
	for _, hint := range []string{".go", ".yml", ".yaml", ".json", ".toml", ".md", ".txt", "\\", "/", "file "} {
		if strings.Contains(prompt, hint) {
			return true
		}
	}
	return false
}
