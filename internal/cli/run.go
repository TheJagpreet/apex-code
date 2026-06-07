// Package cli wires apex-code's invocation modes (one-shot, pipe, interactive
// TUI) on top of the agent loop, provider layer, tool registry, and the
// dynamic tool/skill loading from Phase 8.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/agents"
	"github.com/apex-code/apex/internal/codermode"
	"github.com/apex-code/apex/internal/config"
	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/mcp"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/session"
	"github.com/apex-code/apex/internal/skills"
	"github.com/apex-code/apex/internal/telemetry"
	"github.com/apex-code/apex/internal/tools"
	"github.com/google/uuid"
)

// Config captures the resolved flags/inputs for a single apex invocation.
type Config struct {
	Provider      string
	Model         string
	BaseURL       string
	APIKey        string
	MaxIterations int
	LazyTools     bool
	SkillRoots    []string
	Budget        agent.BudgetFractions
	BudgetSet     bool
	Prompt        string
	CWD           string
	DataDir       string
	Resume        string
	Features      config.Features
	MCPServers    []config.MCPServer
}

// Deps bundles the long-lived collaborators an agent run needs. Building them
// once lets the one-shot, pipe, and interactive modes share identical wiring.
type Deps struct {
	Provider             provider.Provider
	Registry             *tools.Registry
	Dispatcher           *tools.Dispatcher
	Context              *contextmgr.Manager
	Router               *tools.Router
	Lazy                 *tools.LazySet
	Agents               *agents.Loader
	Skills               *skills.Loader
	Sessions             *session.Store
	Telemetry            *telemetry.Store
	TelemetryFiles       *telemetry.FileStore
	Collector            *telemetry.Collector
	Workflows            *codermode.Store
	Coder                *codermode.Engine
	SessionID            string
	SessionTurnSeq       int
	SessionTotalTokens   int
	Initial              []domain.Message
	cfg                  Config
	sessionMu            sync.RWMutex
	extensionMu          sync.RWMutex
	sessionPendingTokens int
	activeAgentName      string
	activeAgent          *agents.Agent
	activatedSkills      map[string]skills.Skill
}

type ExtensionSnapshot struct {
	AvailableAgents  []agents.Header
	AvailableSkills  []skills.Header
	ActiveAgent      string
	ActiveAgentFile  string
	ActiveSkills     []string
	ActiveSkillFiles []string
}

// BuildDeps assembles the provider, tool registry, context manager, and the
// lazy tool/skill machinery for the given config.
func BuildDeps(cfg Config) (*Deps, error) {
	client, err := newProvider(cfg)
	if err != nil {
		return nil, err
	}
	registry := tools.NewDefaultRegistry()
	router := tools.NewRouter(registry)

	agentLoader := agents.NewLoader(filepath.Join(cfg.CWD, ".apex", "agents"))
	_ = agentLoader.Discover()
	loader := skills.NewLoader(cfg.SkillRoots...)
	_ = loader.Discover()

	collector := &telemetry.Collector{}
	ctxMgr := contextmgr.New(client, contextmgr.Options{
		Logger: contextmgr.MultiInstrumenter{collector},
	})

	deps := &Deps{
		Provider:        client,
		Registry:        registry,
		Dispatcher:      tools.NewDispatcher(registry),
		Context:         ctxMgr,
		Router:          router,
		Lazy:            tools.NewLazySet(router),
		Agents:          agentLoader,
		Skills:          loader,
		Collector:       collector,
		cfg:             cfg,
		activatedSkills: map[string]skills.Skill{},
	}

	sessionRoot := sessionArtifactRoot(cfg.DataDir)
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create sessions dir: %w", err)
	}
	if cfg.Features.Sessions {
		store, err := session.Open(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("open session store: %w", err)
		}
		deps.Sessions = store
	}
	if cfg.Features.Telemetry {
		store, err := telemetry.Open(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("open telemetry store: %w", err)
		}
		deps.Telemetry = store
	}
	files, err := telemetry.OpenFileStore(sessionRoot)
	if err != nil {
		return nil, fmt.Errorf("open session file store: %w", err)
	}
	deps.TelemetryFiles = files
	wfStore, err := codermode.OpenStore(sessionRoot)
	if err != nil {
		return nil, fmt.Errorf("open workflow store: %w", err)
	}
	deps.Workflows = wfStore
	deps.Coder = codermode.NewEngine(client, deps.Dispatcher, wfStore, deps.Options)
	deps.Coder.SetTelemetrySink(deps.appendSessionEvent)
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

// EnsureModel verifies the configured model is available when the active
// provider supports proactive validation.
func (d *Deps) EnsureModel(ctx context.Context) error {
	if c, ok := d.Provider.(interface{ EnsureModel(context.Context) error }); ok {
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
	b.WriteString("For remote resources, choose the narrowest tool that matches the job: fetch_web for webpages, fetch_raw for exact remote text/files, fetch_json for JSON APIs, and clone_repo when you need to inspect a remote repository locally.\n")
	if d.cfg.LazyTools {
		b.WriteString("\nAvailable tools (ask for one by name to use it):\n")
		b.WriteString(d.Registry.DescribeDeferred())
		if agentsDoc := d.Agents.Describe(); agentsDoc != "" {
			b.WriteString("\n\nAvailable agents:\n")
			b.WriteString(agentsDoc)
		}
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
		BudgetSet:       d.cfg.BudgetSet,
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
			d.ensureMatchedSkills(lastUserText(messages), d.Lazy)
			return d.Lazy.Specs()
		}
	} else {
		opts.Tools = d.Registry.Specs()
	}
	opts.ExtraSystem = func(messages []domain.Message) []domain.Message {
		return d.extraSystemMessages(messages)
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
	d.resetSessionPendingTokens()
	opts.OnTurn = func(turn agent.Turn, _ agent.BudgetReport, _, _ int) {
		d.addSessionPendingTokens(turn.Response.Usage.TotalTokens)
	}
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
	d.resetSessionPendingTokens()
	return state, err
}

// SystemMessage exposes the run's system message (if any) so callers seeding
// their own conversation can include it.
func (d *Deps) SystemMessage() (domain.Message, bool) {
	return d.systemMessage()
}

func (d *Deps) Extensions() ExtensionSnapshot {
	d.extensionMu.RLock()
	defer d.extensionMu.RUnlock()
	snapshot := ExtensionSnapshot{
		AvailableAgents: append([]agents.Header(nil), d.Agents.Headers()...),
		AvailableSkills: append([]skills.Header(nil), d.Skills.Headers()...),
		ActiveAgent:     d.activeAgentName,
		ActiveSkills:    sortedSkillNames(d.activatedSkills),
	}
	if d.activeAgent != nil {
		snapshot.ActiveAgentFile = d.activeAgent.File()
	}
	for _, name := range snapshot.ActiveSkills {
		skill := d.activatedSkills[name]
		snapshot.ActiveSkillFiles = append(snapshot.ActiveSkillFiles, skill.File())
	}
	return snapshot
}

func (d *Deps) ReloadExtensions() (ExtensionSnapshot, error) {
	d.extensionMu.Lock()
	if err := d.Agents.Discover(); err != nil {
		d.extensionMu.Unlock()
		return ExtensionSnapshot{}, err
	}
	if err := d.Skills.Discover(); err != nil {
		d.extensionMu.Unlock()
		return ExtensionSnapshot{}, err
	}
	if strings.TrimSpace(d.activeAgentName) != "" {
		agent, err := d.Agents.Load(d.activeAgentName)
		if err != nil {
			d.activeAgentName = ""
			d.activeAgent = nil
		} else {
			d.activeAgent = &agent
			d.activeAgentName = agent.Name
		}
	}
	nextSkills := map[string]skills.Skill{}
	for name := range d.activatedSkills {
		if skill, err := d.Skills.Load(name); err == nil {
			nextSkills[skill.Name] = skill
		}
	}
	d.activatedSkills = nextSkills
	snapshot := ExtensionSnapshot{
		AvailableAgents: append([]agents.Header(nil), d.Agents.Headers()...),
		AvailableSkills: append([]skills.Header(nil), d.Skills.Headers()...),
		ActiveAgent:     d.activeAgentName,
		ActiveSkills:    sortedSkillNames(d.activatedSkills),
	}
	if d.activeAgent != nil {
		snapshot.ActiveAgentFile = d.activeAgent.File()
	}
	for _, name := range snapshot.ActiveSkills {
		skill := d.activatedSkills[name]
		snapshot.ActiveSkillFiles = append(snapshot.ActiveSkillFiles, skill.File())
	}
	d.extensionMu.Unlock()
	return snapshot, nil
}

func (d *Deps) SetActiveAgent(name string) error {
	d.extensionMu.Lock()
	defer d.extensionMu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		d.activeAgentName = ""
		d.activeAgent = nil
		return nil
	}
	agent, err := d.Agents.Load(name)
	if err != nil {
		return err
	}
	d.activeAgentName = agent.Name
	d.activeAgent = &agent
	return nil
}

func (d *Deps) extraSystemMessages(messages []domain.Message) []domain.Message {
	d.ensureMatchedSkills(lastUserText(messages), nil)
	d.extensionMu.RLock()
	defer d.extensionMu.RUnlock()
	var out []domain.Message
	if d.activeAgent != nil && strings.TrimSpace(d.activeAgent.Prompt) != "" {
		out = append(out, domain.Message{
			Role:    domain.RoleSystem,
			Content: "Active custom agent `" + d.activeAgent.Name + "` from " + d.activeAgent.File() + ":\n\n" + d.activeAgent.Prompt,
		})
	}
	for _, name := range sortedSkillNames(d.activatedSkills) {
		skill := d.activatedSkills[name]
		if strings.TrimSpace(skill.Prompt) == "" {
			continue
		}
		out = append(out, domain.Message{
			Role:    domain.RoleSystem,
			Content: "Active custom skill `" + skill.Name + "` from " + skill.File() + ":\n\n" + skill.Prompt,
		})
	}
	return out
}

func (d *Deps) markSkillActivated(skill skills.Skill) {
	d.extensionMu.Lock()
	defer d.extensionMu.Unlock()
	if d.activatedSkills == nil {
		d.activatedSkills = map[string]skills.Skill{}
	}
	d.activatedSkills[skill.Name] = skill
}

func (d *Deps) ensureMatchedSkills(text string, set *tools.LazySet) {
	for _, name := range d.Skills.Match(text) {
		skill, err := d.Skills.Activate(name, set)
		if err != nil {
			continue
		}
		d.markSkillActivated(skill)
	}
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
	d.loadSessionTelemetryState(ctx)
	rehydrated := d.Context.Rehydrate(snap.WorkingSet)
	d.Initial = d.Context.Messages(rehydrated)
	return nil
}

func (d *Deps) persistRun(ctx context.Context, inputMessages []domain.Message, state agent.LoopState) error {
	sessionID := d.ensureSessionID()
	savedBy := d.Collector.LastSavedBy()
	extensions := d.Extensions()
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
			SessionID:   sessionID,
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
	for _, turn := range state.Turns {
		output := cloneMessages([]domain.Message{turn.Response.Message})
		var outputMessage *domain.Message
		if len(output) == 1 {
			outputMessage = &output[0]
		}
		if err := d.appendSessionEvent(ctx, telemetry.SessionEvent{
			Mode:             "chat",
			Kind:             "llm_turn",
			Model:            d.cfg.Model,
			PromptTokens:     turn.Response.Usage.PromptTokens,
			CompletionTokens: turn.Response.Usage.CompletionTokens,
			TotalTokens:      turn.Response.Usage.TotalTokens,
			CacheCreation:    turn.Response.Usage.CacheCreationTokens,
			CacheRead:        turn.Response.Usage.CacheReadTokens,
			DurationMs:       turn.Duration.Milliseconds(),
			Termination:      string(turn.Response.StopReason),
			ToolCalls:        toolCallNames(turn.ToolCalls),
			ToolCallDetails:  cloneToolCalls(turn.ToolCalls),
			ToolResults:      len(turn.ToolResults),
			InputMessages:    cloneMessages(turn.Request.Messages),
			OutputMessage:    outputMessage,
			SavedBy:          savedBy,
			Error:            errorString(turn.Err),
			CustomAgent:      extensions.ActiveAgent,
			CustomAgentFile:  extensions.ActiveAgentFile,
			CustomSkills:     append([]string(nil), extensions.ActiveSkills...),
			CustomSkillFiles: append([]string(nil), extensions.ActiveSkillFiles...),
		}); err != nil {
			return err
		}
		for i, call := range turn.ToolCalls {
			var resultDetails []domain.ToolResult
			if i < len(turn.ToolResults) {
				resultDetails = cloneToolResults([]domain.ToolResult{turn.ToolResults[i]})
			}
			outcome, recoverable := telemetry.ToolExecOutcome(resultDetails)
			if err := d.appendSessionEvent(ctx, telemetry.SessionEvent{
				Mode:              "chat",
				Kind:              "tool_exec",
				Outcome:           outcome,
				Recoverable:       recoverable,
				Model:             d.cfg.Model,
				DurationMs:        turn.ToolDuration.Milliseconds(),
				Termination:       string(turn.Response.StopReason),
				ToolCalls:         toolCallNames([]domain.ToolCall{call}),
				ToolCallDetails:   cloneToolCalls([]domain.ToolCall{call}),
				ToolResults:       len(resultDetails),
				ToolResultDetails: resultDetails,
				CustomAgent:       extensions.ActiveAgent,
				CustomAgentFile:   extensions.ActiveAgentFile,
				CustomSkills:      append([]string(nil), extensions.ActiveSkills...),
				CustomSkillFiles:  append([]string(nil), extensions.ActiveSkillFiles...),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Deps) effectiveSessionID() string {
	return d.ensureSessionID()
}

func (d *Deps) ensureSessionID() string {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	if strings.TrimSpace(d.SessionID) == "" {
		d.SessionID = uuid.NewString()
		d.SessionTurnSeq = 0
		d.SessionTotalTokens = 0
		d.sessionPendingTokens = 0
	}
	return d.SessionID
}

func (d *Deps) loadSessionTelemetryState(ctx context.Context) {
	d.sessionMu.Lock()
	d.SessionTurnSeq = 0
	d.SessionTotalTokens = 0
	d.sessionPendingTokens = 0
	d.sessionMu.Unlock()
	if d.TelemetryFiles == nil || strings.TrimSpace(d.SessionID) == "" {
		return
	}
	totals, count, err := d.TelemetryFiles.SessionTotals(ctx, d.SessionID)
	if err != nil {
		return
	}
	d.sessionMu.Lock()
	d.SessionTurnSeq = count
	d.SessionTotalTokens = totals.TotalTokens
	d.sessionMu.Unlock()
}

func (d *Deps) appendSessionEvent(ctx context.Context, event telemetry.SessionEvent) error {
	sessionID := d.ensureSessionID()
	d.sessionMu.Lock()
	d.SessionTurnSeq++
	event.Index = d.SessionTurnSeq
	d.sessionMu.Unlock()
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if strings.TrimSpace(event.Model) == "" {
		event.Model = d.cfg.Model
	}
	if strings.TrimSpace(event.Mode) == "" {
		event.Mode = "chat"
	}
	if d.TelemetryFiles != nil {
		if err := d.TelemetryFiles.AppendEvent(ctx, sessionID, telemetry.FileMeta{
			Model: d.cfg.Model,
			CWD:   d.cfg.CWD,
		}, event); err != nil {
			return err
		}
	}
	d.sessionMu.Lock()
	d.SessionTotalTokens += event.TotalTokens
	if d.sessionPendingTokens >= event.TotalTokens {
		d.sessionPendingTokens -= event.TotalTokens
	} else {
		d.sessionPendingTokens = 0
	}
	d.sessionMu.Unlock()
	return nil
}

func (d *Deps) addSessionPendingTokens(tokens int) {
	if tokens <= 0 {
		return
	}
	d.sessionMu.Lock()
	d.sessionPendingTokens += tokens
	d.sessionMu.Unlock()
}

func (d *Deps) resetSessionPendingTokens() {
	d.sessionMu.Lock()
	d.sessionPendingTokens = 0
	d.sessionMu.Unlock()
}

func (d *Deps) currentSessionTokens() int {
	d.sessionMu.RLock()
	defer d.sessionMu.RUnlock()
	return d.SessionTotalTokens + d.sessionPendingTokens
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

func sortedSkillNames(items map[string]skills.Skill) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

func sessionArtifactRoot(dataDir string) string {
	if strings.TrimSpace(dataDir) != "" {
		return filepath.Join(filepath.Clean(dataDir), "sessions")
	}
	return filepath.Join(".", "sessions")
}

func cloneMessages(messages []domain.Message) []domain.Message {
	out := make([]domain.Message, 0, len(messages))
	for _, m := range messages {
		out = append(out, domain.Message{
			Role:         m.Role,
			Content:      m.Content,
			ToolCalls:    cloneToolCalls(m.ToolCalls),
			ToolResults:  cloneToolResults(m.ToolResults),
			CacheControl: m.CacheControl,
		})
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
