package contextmgr_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/fake"
)

func TestRenderFitsBudgetAndKeepsPinnedItems(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "summary of older turns"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	}).WithCapabilities(provider.Caps{ContextWindow: 96, MaxOutputTokens: 24})
	mgr := contextmgr.New(p, contextmgr.Options{})
	budget := testBudget(96, 24)

	ws := mgr.FromMessages([]domain.Message{
		{Role: domain.RoleSystem, Content: "system instructions stay pinned"},
		{Role: domain.RoleUser, Content: strings.Repeat("older user details ", 20)},
		{Role: domain.RoleAssistant, Content: strings.Repeat("older assistant details ", 20)},
		{Role: domain.RoleTool, ToolResults: []domain.ToolResult{{ToolCallID: "call_1", Content: strings.Repeat("tool output ", 30)}}},
		{Role: domain.RoleUser, Content: "latest user request stays"},
	})

	prompt, err := mgr.Render(context.Background(), ws, budget)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if prompt.Report.TokensOut > budget.PromptLimit {
		t.Fatalf("tokens out = %d, limit = %d", prompt.Report.TokensOut, budget.PromptLimit)
	}
	if len(prompt.Messages) == 0 || prompt.Messages[0].Role != domain.RoleSystem {
		t.Fatalf("system message not preserved: %+v", prompt.Messages)
	}
	if !containsMessage(prompt.Messages, "latest user request stays") {
		t.Fatalf("latest pinned user message missing: %+v", prompt.Messages)
	}
}

func TestRenderDigestsAndElidesCompactly(t *testing.T) {
	p := fake.New(nil)
	mgr := contextmgr.New(p, contextmgr.Options{MaxDigestChars: 80})
	msg := domain.Message{Role: domain.RoleTool, ToolResults: []domain.ToolResult{{ToolCallID: "call_1", Content: strings.Repeat("same output ", 30)}}}
	ws := mgr.FromMessages([]domain.Message{
		{Role: domain.RoleUser, Content: "start"},
		msg,
		msg,
		{Role: domain.RoleUser, Content: "finish"},
	})

	prompt, err := mgr.Render(context.Background(), ws, testBudget(256, 32))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if prompt.Report.SavedBy["digest"] == 0 {
		t.Fatalf("digest did not save tokens: %+v", prompt.Report)
	}
	if len(prompt.Report.Elided) == 0 {
		t.Fatalf("duplicate output was not elided: %+v", prompt.Report)
	}
	if strings.Contains(joinMessages(prompt.Messages), strings.Repeat("same output ", 20)) {
		t.Fatalf("raw repeated output leaked into prompt: %q", joinMessages(prompt.Messages))
	}
}

func TestCompactorSatisfiesAgentHook(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "summary"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	})
	mgr := contextmgr.New(p, contextmgr.Options{})
	compacted, err := mgr.Compactor().Compact(context.Background(), []domain.Message{
		{Role: domain.RoleSystem, Content: "system"},
		{Role: domain.RoleUser, Content: strings.Repeat("history ", 100)},
		{Role: domain.RoleUser, Content: "latest"},
	}, agent.BudgetReport{}, testBudget(80, 20))
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !containsMessage(compacted, "latest") {
		t.Fatalf("latest message missing after compaction: %+v", compacted)
	}
}

func TestRollingSummaryIsCached(t *testing.T) {
	p := fake.New([]provider.StreamEvent{
		{Kind: provider.EventText, Text: "cached compact story"},
		{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
	})
	mgr := contextmgr.New(p, contextmgr.Options{})
	ws := mgr.FromMessages([]domain.Message{
		{Role: domain.RoleSystem, Content: "system"},
		{Role: domain.RoleUser, Content: strings.Repeat("alpha ", 80)},
		{Role: domain.RoleAssistant, Content: strings.Repeat("beta ", 80)},
		{Role: domain.RoleUser, Content: strings.Repeat("gamma ", 80)},
		{Role: domain.RoleUser, Content: "latest"},
	})
	budget := testBudget(256, 32)

	first, err := mgr.Render(context.Background(), ws, budget)
	if err != nil {
		t.Fatalf("first Render: %v", err)
	}
	second, err := mgr.Render(context.Background(), ws, budget)
	if err != nil {
		t.Fatalf("second Render: %v", err)
	}
	if len(first.Report.Summarized) == 0 || first.Report.SavedBy["summary"] == 0 {
		t.Fatalf("summary not used: %+v", first.Report)
	}
	if joinMessages(first.Messages) != joinMessages(second.Messages) {
		t.Fatalf("cached summary changed output")
	}
	if got := len(p.Requests()); got != 1 {
		t.Fatalf("summary cache misses = %d provider calls", got)
	}
}

func TestRenderIsIdempotentAndLogsSavings(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	p := fake.New(nil)
	mgr := contextmgr.New(p, contextmgr.Options{
		Logger: contextmgr.SlogInstrumenter{Logger: logger},
	})
	ws := mgr.FromMessages([]domain.Message{
		{Role: domain.RoleSystem, Content: "system"},
		{Role: domain.RoleUser, Content: "same"},
		{Role: domain.RoleUser, Content: "same"},
	})
	budget := testBudget(256, 32)

	first, err := mgr.Render(context.Background(), ws, budget)
	if err != nil {
		t.Fatalf("first Render: %v", err)
	}
	second, err := mgr.Render(context.Background(), ws, budget)
	if err != nil {
		t.Fatalf("second Render: %v", err)
	}
	if joinMessages(first.Messages) != joinMessages(second.Messages) {
		t.Fatalf("render not idempotent:\nfirst=%q\nsecond=%q", joinMessages(first.Messages), joinMessages(second.Messages))
	}
	if !strings.Contains(buf.String(), "tokens_saved") {
		t.Fatalf("render metrics not logged: %q", buf.String())
	}
}

func testBudget(window, headroom int) agent.Budget {
	return agent.Budget{
		TotalWindow:    window,
		PromptLimit:    window - headroom,
		OutputHeadroom: headroom,
		Pools: map[agent.PoolName]int{
			agent.PoolSystem:         window / 4,
			agent.PoolTools:          window / 8,
			agent.PoolHistory:        window / 2,
			agent.PoolRetrieved:      window / 8,
			agent.PoolWorkingFiles:   window / 8,
			agent.PoolOutputHeadroom: headroom,
		},
	}
}

func containsMessage(messages []domain.Message, want string) bool {
	return strings.Contains(joinMessages(messages), want)
}

func joinMessages(messages []domain.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(msg.Content)
		for _, result := range msg.ToolResults {
			b.WriteString(result.Content)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
