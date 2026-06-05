package agent_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/fake"
)

func TestExecuteTurnCollectsSingleTurn(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "Hello"},
		{Kind: provider.EventText, Text: ", world"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn, Usage: &domain.Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}},
	})

	turn, err := agent.New(p, nil).ExecuteTurn(context.Background(), domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if turn.Response.Message.Content != "Hello, world" {
		t.Fatalf("content = %q", turn.Response.Message.Content)
	}
	if turn.Response.StopReason != domain.StopEndTurn {
		t.Fatalf("stop = %q", turn.Response.StopReason)
	}
}

func TestRunTerminatesWithFinalAnswer(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "Done"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	})

	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "say done"},
	}, agent.Options{MaxIterations: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.TerminationReason != agent.TerminationFinalAnswer {
		t.Fatalf("termination = %q", state.TerminationReason)
	}
	if state.FinalResponse == nil || state.FinalResponse.Message.Content != "Done" {
		t.Fatalf("final response = %+v", state.FinalResponse)
	}
}

func TestRunToolUseObserveRepeat(t *testing.T) {
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_1", Name: "grep", Arguments: []byte(`{"pattern":"TODO"}`)}},
			{Kind: provider.EventDone, StopReason: domain.StopToolUse},
		},
		{
			{Kind: provider.EventText, Text: "Applied tool output"},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})

	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "find todos"},
	}, agent.Options{MaxIterations: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(state.Turns) != 2 {
		t.Fatalf("turns = %d", len(state.Turns))
	}
	if len(state.Turns[0].ToolResults) != 1 || !state.Turns[0].ToolResults[0].IsError {
		t.Fatalf("tool results = %+v", state.Turns[0].ToolResults)
	}
	requests := p.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if len(requests[1].Messages) < 3 {
		t.Fatalf("observe messages = %d", len(requests[1].Messages))
	}
}

func TestRunMaxIterationsTerminates(t *testing.T) {
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_1", Name: "grep", Arguments: []byte(`{"pattern":"TODO"}`)}},
			{Kind: provider.EventDone, StopReason: domain.StopToolUse},
		},
	})

	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "find todos"},
	}, agent.Options{MaxIterations: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.TerminationReason != agent.TerminationMaxIterations {
		t.Fatalf("termination = %q", state.TerminationReason)
	}
}

func TestRunDefaultMaxIterationsAllowsToolThenFinal(t *testing.T) {
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_1", Name: "grep", Arguments: []byte(`{"pattern":"TODO"}`)}},
			{Kind: provider.EventDone, StopReason: domain.StopToolUse},
		},
		{
			{Kind: provider.EventText, Text: "done"},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "find todos"},
	}, agent.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.TerminationReason != agent.TerminationFinalAnswer {
		t.Fatalf("termination = %q", state.TerminationReason)
	}
}

func TestRunUsesNewDefaultMaxIterations(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "ok"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	})
	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "hi"},
	}, agent.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.MaxIterations != 50 {
		t.Fatalf("default max iterations = %d", state.MaxIterations)
	}
}

func TestRunProviderErrorTerminates(t *testing.T) {
	want := errors.New("boom")
	p := fake.New(nil).WithCompleteError(want)

	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "hi"},
	}, agent.Options{MaxIterations: 2})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if state.TerminationReason != agent.TerminationError {
		t.Fatalf("termination = %q", state.TerminationReason)
	}
}

func TestRunUserCancelTerminates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	state, err := agent.New(fake.New(nil), nil).Run(ctx, []domain.Message{
		{Role: domain.RoleUser, Content: "hi"},
	}, agent.Options{MaxIterations: 2})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if state.TerminationReason != agent.TerminationUserCancel {
		t.Fatalf("termination = %q", state.TerminationReason)
	}
}

func TestRunBudgetUsesCompactorAndLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "ok"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	}).WithCapabilities(provider.Caps{
		ContextWindow:     32,
		MaxOutputTokens:   8,
		SupportsStreaming: true,
	})
	compactor := &recordingCompactor{
		out: []domain.Message{{Role: domain.RoleUser, Content: "tiny"}},
	}

	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleSystem, Content: strings.Repeat("system", 8)},
		{Role: domain.RoleUser, Content: strings.Repeat("history", 8)},
	}, agent.Options{
		MaxIterations: 1,
		Compactor:     compactor,
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactor.calls == 0 {
		t.Fatal("compactor was not called")
	}
	if state.LastBudget.OutputHeadroom < 8 {
		t.Fatalf("headroom = %d", state.LastBudget.OutputHeadroom)
	}
	if !strings.Contains(buf.String(), "\"tokens_by_pool\"") {
		t.Fatalf("log output = %q", buf.String())
	}
}

func TestRunWithoutExplicitBudgetUsesLargePromptWindow(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "ok"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	}).WithCapabilities(provider.Caps{
		ContextWindow:     8192,
		MaxOutputTokens:   4096,
		SupportsStreaming: true,
	})

	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "hi"},
	}, agent.Options{MaxIterations: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.LastBudget.PromptLimit != 7168 {
		t.Fatalf("prompt limit = %d", state.LastBudget.PromptLimit)
	}
	if state.LastBudget.OutputHeadroom != 1024 {
		t.Fatalf("headroom = %d", state.LastBudget.OutputHeadroom)
	}
}

func TestRunWithExplicitBudgetStillUsesProviderMaxOutputHeadroom(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "ok"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	}).WithCapabilities(provider.Caps{
		ContextWindow:     8192,
		MaxOutputTokens:   4096,
		SupportsStreaming: true,
	})

	state, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: "hi"},
	}, agent.Options{
		MaxIterations:   1,
		BudgetSet:       true,
		BudgetFractions: agent.DefaultBudgetFractions(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.LastBudget.PromptLimit != 4096 {
		t.Fatalf("prompt limit = %d", state.LastBudget.PromptLimit)
	}
	if state.LastBudget.OutputHeadroom != 4096 {
		t.Fatalf("headroom = %d", state.LastBudget.OutputHeadroom)
	}
}

func TestRunBudgetWithoutCompactorFails(t *testing.T) {
	p := fake.New(nil).WithCapabilities(provider.Caps{
		ContextWindow:     32,
		MaxOutputTokens:   8,
		SupportsStreaming: true,
	})

	_, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleUser, Content: strings.Repeat("overflow", 20)},
	}, agent.Options{MaxIterations: 1})
	if !errors.Is(err, agent.ErrBudgetExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunUsesPromptAssemblerMetadata(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "ok"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	})
	_, err := agent.New(p, nil).Run(context.Background(), []domain.Message{
		{Role: domain.RoleSystem, Content: "stable system"},
		{Role: domain.RoleUser, Content: "latest"},
	}, agent.Options{
		Tools:       []domain.ToolSpec{{Name: "read_file", Description: "Read a compact file range."}},
		PromptCache: true,
		KeepAlive:   "10m",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requests := p.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	req := requests[0]
	if req.KeepAlive != "10m" {
		t.Fatalf("keep alive = %q", req.KeepAlive)
	}
	if len(req.Messages) < 3 || !strings.Contains(req.Messages[1].Content, "tools:") {
		t.Fatalf("assembled messages = %+v", req.Messages)
	}
	if req.Messages[1].CacheControl != "ephemeral" {
		t.Fatalf("cache control = %+v", req.Messages)
	}
}

type recordingCompactor struct {
	calls int
	out   []domain.Message
}

func (r *recordingCompactor) Compact(_ context.Context, _ []domain.Message, _ agent.BudgetReport, _ agent.Budget) ([]domain.Message, error) {
	r.calls++
	out := make([]domain.Message, len(r.out))
	copy(out, r.out)
	return out, nil
}
