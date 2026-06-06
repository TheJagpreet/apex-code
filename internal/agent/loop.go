package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/promptasm"
	"github.com/apex-code/apex/internal/provider"
)

type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseAssemble Phase = "assemble"
	PhaseCall     Phase = "call"
	PhaseParse    Phase = "parse"
	PhaseAct      Phase = "act"
	PhaseObserve  Phase = "observe"
	PhaseDone     Phase = "done"
)

type TerminationReason string

const (
	TerminationNone          TerminationReason = ""
	TerminationFinalAnswer   TerminationReason = "final_answer"
	TerminationMaxIterations TerminationReason = "max_iterations"
	TerminationError         TerminationReason = "error"
	TerminationUserCancel    TerminationReason = "user_cancel"
)

// Turn captures one provider round trip and any tool work that followed it.
type Turn struct {
	Request      domain.Request
	Response     domain.Response
	ToolCalls    []domain.ToolCall
	ToolResults  []domain.ToolResult
	ToolDuration time.Duration
	Budget       BudgetReport
	Duration     time.Duration
	Err          error
}

// LoopState tracks where the agent loop is and what has happened so far.
type LoopState struct {
	Phase             Phase
	Iteration         int
	MaxIterations     int
	TerminationReason TerminationReason
	Messages          []domain.Message
	Turns             []Turn
	FinalResponse     *domain.Response
	LastError         error
	LastBudget        BudgetReport
}

type Options struct {
	Model           string
	Tools           []domain.ToolSpec
	Temperature     float64
	MaxTokens       int
	Stop            []string
	KeepAlive       string
	PromptCache     bool
	MaxIterations   int
	BudgetFractions BudgetFractions
	BudgetSet       bool
	Logger          *slog.Logger
	Compactor       Compactor
	OnTurn          func(Turn, BudgetReport, int, int)
	OnToolResults   func(Turn, int, int)

	// StreamText, when set, is invoked with each assistant text delta as it is
	// received from the provider, enabling live streaming UIs. It does not change
	// the final collected response. It may be called from the provider goroutine.
	StreamText func(string)

	// ToolProvider, when set, is consulted at the start of every iteration to
	// decide which full tool schemas to advertise for that turn. It receives
	// the conversation so far and returns the specs to inject into the next
	// turn's tools pool. This powers dynamic/lazy tool loading (plan 8.3):
	// the system prompt advertises only deferred descriptors, and full schemas
	// are paid for a turn at a time. When nil, Options.Tools is used as-is.
	ToolProvider func(messages []domain.Message) []domain.ToolSpec
}

type ToolDispatcher interface {
	DispatchToolCalls(ctx context.Context, calls []domain.ToolCall) ([]domain.ToolResult, error)
}

// StubToolDispatcher is the Phase 2 placeholder until the real tool registry
// arrives. It turns every tool request into a structured tool error result so
// the loop can keep progressing deterministically in tests.
type StubToolDispatcher struct{}

func (StubToolDispatcher) DispatchToolCalls(_ context.Context, calls []domain.ToolCall) ([]domain.ToolResult, error) {
	results := make([]domain.ToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, domain.ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("tool dispatch not implemented yet for %q", call.Name),
			IsError:    true,
		})
	}
	return results, nil
}

type Loop struct {
	provider provider.Provider
	tools    ToolDispatcher
}

func New(p provider.Provider, tools ToolDispatcher) *Loop {
	if tools == nil {
		tools = StubToolDispatcher{}
	}
	return &Loop{provider: p, tools: tools}
}

// ExecuteTurn runs a single assembled request through the provider and returns
// the fully collected assistant response plus any requested tool calls.
func (l *Loop) ExecuteTurn(ctx context.Context, req domain.Request) (Turn, error) {
	return l.executeTurn(ctx, req, nil)
}

func (l *Loop) executeTurn(ctx context.Context, req domain.Request, onText func(string)) (turn Turn, err error) {
	turn.Request = cloneRequest(req)
	started := time.Now()
	defer func() { turn.Duration = time.Since(started) }()

	stream, err := l.provider.Complete(ctx, req)
	if err != nil {
		turn.Err = err
		return turn, err
	}
	defer stream.Close()

	resp, toolCalls, err := collectResponse(stream, onText)
	if err != nil {
		turn.Err = err
		return turn, err
	}

	turn.Response = resp
	turn.ToolCalls = toolCalls
	return turn, nil
}

func (l *Loop) Run(ctx context.Context, messages []domain.Message, opts Options) (LoopState, error) {
	state := LoopState{
		Phase:         PhaseIdle,
		MaxIterations: opts.MaxIterations,
		Messages:      cloneMessages(messages),
	}
	if state.MaxIterations <= 0 {
		state.MaxIterations = 50
	}

	for {
		if err := ctx.Err(); err != nil {
			state.Phase = PhaseDone
			state.TerminationReason = TerminationUserCancel
			state.LastError = err
			return state, err
		}

		if state.Iteration >= state.MaxIterations {
			state.Phase = PhaseDone
			state.TerminationReason = TerminationMaxIterations
			return state, nil
		}

		// Dynamic tool injection: recompute the advertised schemas from the
		// conversation so far before assembling this turn (plan 8.3).
		if opts.ToolProvider != nil {
			opts.Tools = opts.ToolProvider(state.Messages)
		}

		state.Phase = PhaseAssemble
		messages, budgetReport, err := l.prepareMessages(ctx, state.Messages, opts)
		state.LastBudget = budgetReport
		if err != nil {
			state.Phase = PhaseDone
			state.TerminationReason = TerminationError
			state.LastError = err
			return state, err
		}
		state.Messages = cloneMessages(messages)
		req := assembleRequest(messages, opts)

		state.Phase = PhaseCall
		turn, err := l.executeTurn(ctx, req, opts.StreamText)
		state.Iteration++
		turn.Budget = budgetReport
		state.Turns = append(state.Turns, turn)
		if opts.OnTurn != nil {
			opts.OnTurn(turn, budgetReport, state.Iteration, state.MaxIterations)
		}
		if err != nil {
			state.Phase = PhaseDone
			state.TerminationReason = TerminationError
			state.LastError = err
			return state, err
		}

		state.Phase = PhaseParse
		if len(turn.ToolCalls) == 0 {
			state.Messages = append(state.Messages, turn.Response.Message)
			final := turn.Response
			state.FinalResponse = &final
			state.Phase = PhaseDone
			state.TerminationReason = TerminationFinalAnswer
			return state, nil
		}

		state.Phase = PhaseAct
		toolStarted := time.Now()
		results, err := l.tools.DispatchToolCalls(ctx, turn.ToolCalls)
		turn.ToolDuration = time.Since(toolStarted)
		turn.ToolResults = cloneToolResults(results)
		state.Turns[len(state.Turns)-1] = turn
		if opts.OnToolResults != nil {
			opts.OnToolResults(turn, state.Iteration, state.MaxIterations)
		}
		if err != nil {
			state.Phase = PhaseDone
			state.TerminationReason = TerminationError
			state.LastError = err
			return state, err
		}

		state.Phase = PhaseObserve
		state.Messages = observeTurn(state.Messages, turn)
	}
}

func assembleRequest(messages []domain.Message, opts Options) domain.Request {
	out, err := promptasm.New().Assemble(context.Background(), promptasm.Input{
		Model:       opts.Model,
		System:      systemMessages(messages),
		Tools:       cloneToolSpecs(opts.Tools),
		History:     historyMessages(messages),
		LatestUser:  latestUserMessage(messages),
		FreshTool:   freshToolMessages(messages),
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
		Stop:        append([]string(nil), opts.Stop...),
		KeepAlive:   opts.KeepAlive,
		PromptCache: opts.PromptCache,
	})
	if err != nil {
		return domain.Request{
			Model:       opts.Model,
			Messages:    cloneMessages(messages),
			Tools:       cloneToolSpecs(opts.Tools),
			Temperature: opts.Temperature,
			MaxTokens:   opts.MaxTokens,
			Stop:        append([]string(nil), opts.Stop...),
			KeepAlive:   opts.KeepAlive,
		}
	}
	return out.Request
}

func systemMessages(messages []domain.Message) []domain.Message {
	out := make([]domain.Message, 0)
	for _, msg := range messages {
		if msg.Role == domain.RoleSystem {
			out = append(out, msg)
		}
	}
	return out
}

func historyMessages(messages []domain.Message) []domain.Message {
	latestUser := latestUserIndex(messages)
	freshStart := freshToolStart(messages)
	out := make([]domain.Message, 0)
	for i, msg := range messages {
		if msg.Role == domain.RoleSystem || i == latestUser || i >= freshStart {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func latestUserMessage(messages []domain.Message) domain.Message {
	idx := latestUserIndex(messages)
	if idx < 0 {
		return domain.Message{}
	}
	return messages[idx]
}

func freshToolMessages(messages []domain.Message) []domain.Message {
	start := freshToolStart(messages)
	if start >= len(messages) {
		return nil
	}
	return cloneMessages(messages[start:])
}

func latestUserIndex(messages []domain.Message) int {
	freshStart := freshToolStart(messages)
	for i := freshStart - 1; i >= 0; i-- {
		if messages[i].Role == domain.RoleUser {
			return i
		}
	}
	return -1
}

func freshToolStart(messages []domain.Message) int {
	i := len(messages)
	for i > 0 && messages[i-1].Role == domain.RoleTool {
		i--
	}
	if i > 0 && i < len(messages) && messages[i-1].Role == domain.RoleAssistant && len(messages[i-1].ToolCalls) > 0 {
		i--
	}
	return i
}

func (l *Loop) prepareMessages(ctx context.Context, messages []domain.Message, opts Options) ([]domain.Message, BudgetReport, error) {
	budget := buildBudget(l.provider.Capabilities(), opts)
	current := cloneMessages(messages)

	for attempts := 0; attempts < 4; attempts++ {
		req := assembleRequest(current, opts)
		report, err := measureBudget(ctx, l.provider, req, budget)
		if err != nil {
			return nil, BudgetReport{}, err
		}
		logBudget(opts.Logger, report)
		if report.WithinBudget {
			return current, report, nil
		}

		if opts.Compactor == nil {
			return nil, report, fmt.Errorf("%w: prompt=%d limit=%d", ErrBudgetExceeded, report.TotalPromptTokens, report.PromptLimit)
		}

		compacted, err := opts.Compactor.Compact(ctx, current, report, budget)
		if err != nil {
			return nil, report, err
		}
		if sameMessages(current, compacted) {
			return nil, report, fmt.Errorf("%w: compactor made no progress", ErrBudgetExceeded)
		}
		current = cloneMessages(compacted)
	}

	finalReport, err := measureBudget(ctx, l.provider, assembleRequest(current, opts), budget)
	if err != nil {
		return nil, BudgetReport{}, err
	}
	return nil, finalReport, fmt.Errorf("%w: compactor attempts exhausted", ErrBudgetExceeded)
}

func collectResponse(stream provider.Stream, onText func(string)) (domain.Response, []domain.ToolCall, error) {
	var (
		resp      domain.Response
		text      string
		toolCalls []domain.ToolCall
	)

	resp.Message.Role = domain.RoleAssistant

	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return domain.Response{}, nil, err
		}

		switch ev.Kind {
		case provider.EventText:
			text += ev.Text
			if onText != nil && ev.Text != "" {
				onText(ev.Text)
			}
		case provider.EventToolCall:
			if ev.ToolCall != nil {
				toolCalls = append(toolCalls, *ev.ToolCall)
			}
		case provider.EventUsage:
			if ev.Usage != nil {
				resp.Usage = *ev.Usage
			}
		case provider.EventDone:
			resp.StopReason = ev.StopReason
			if ev.Usage != nil {
				resp.Usage = *ev.Usage
			}
		}
	}

	resp.Message.Content = text
	resp.Message.ToolCalls = cloneToolCalls(toolCalls)
	return resp, toolCalls, nil
}

func observeTurn(messages []domain.Message, turn Turn) []domain.Message {
	out := cloneMessages(messages)
	out = append(out, turn.Response.Message)
	if len(turn.ToolResults) > 0 {
		out = append(out, domain.Message{
			Role:        domain.RoleTool,
			ToolResults: cloneToolResults(turn.ToolResults),
		})
	}
	return out
}

func cloneRequest(req domain.Request) domain.Request {
	return domain.Request{
		Model:       req.Model,
		Messages:    cloneMessages(req.Messages),
		Tools:       cloneToolSpecs(req.Tools),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stop:        append([]string(nil), req.Stop...),
		KeepAlive:   req.KeepAlive,
	}
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

func cloneToolSpecs(specs []domain.ToolSpec) []domain.ToolSpec {
	out := make([]domain.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		out = append(out, domain.ToolSpec{
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  append([]byte(nil), spec.Parameters...),
		})
	}
	return out
}

func sameMessages(a, b []domain.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content {
			return false
		}
		if len(a[i].ToolCalls) != len(b[i].ToolCalls) || len(a[i].ToolResults) != len(b[i].ToolResults) {
			return false
		}
		for j := range a[i].ToolCalls {
			if a[i].ToolCalls[j].ID != b[i].ToolCalls[j].ID ||
				a[i].ToolCalls[j].Name != b[i].ToolCalls[j].Name ||
				string(a[i].ToolCalls[j].Arguments) != string(b[i].ToolCalls[j].Arguments) {
				return false
			}
		}
		for j := range a[i].ToolResults {
			if a[i].ToolResults[j] != b[i].ToolResults[j] {
				return false
			}
		}
	}
	return true
}
