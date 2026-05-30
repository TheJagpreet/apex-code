package telemetry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/telemetry"
)

func TestTelemetryTotalsAndFormatting(t *testing.T) {
	store, err := telemetry.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()

	if err := store.SaveTurn(context.Background(), telemetry.TurnMetric{
		SessionID:        "sess-1",
		TurnIndex:        1,
		Model:            "gemma4:e2b",
		PromptTokens:     100,
		CompletionTokens: 40,
		TotalTokens:      140,
		CacheCreation:    20,
		CacheRead:        10,
		Termination:      "tool_use",
		SavedBy:          map[string]int{"context": 55, "lazy_tools": 12},
	}); err != nil {
		t.Fatalf("save turn 1: %v", err)
	}
	if err := store.SaveTurn(context.Background(), telemetry.TurnMetric{
		SessionID:        "sess-1",
		TurnIndex:        2,
		Model:            "gemma4:e2b",
		PromptTokens:     80,
		CompletionTokens: 20,
		TotalTokens:      100,
		CacheCreation:    0,
		CacheRead:        30,
		Termination:      "final_answer",
		SavedBy:          map[string]int{"context": 5},
	}); err != nil {
		t.Fatalf("save turn 2: %v", err)
	}

	total, err := store.Totals(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if total.Turns != 2 || total.TotalTokens != 240 || total.SavedBy["context"] != 60 {
		t.Fatalf("totals=%+v", total)
	}
	if ratio := telemetry.CacheHitRatio(total); ratio <= 0.66 || ratio >= 0.67 {
		t.Fatalf("cache ratio = %f", ratio)
	}
	out := telemetry.FormatTotals(total)
	if !strings.Contains(out, "turns=2") || !strings.Contains(out, "saved[context=60, lazy_tools=12]") {
		t.Fatalf("formatted = %q", out)
	}
}

func TestTelemetryDimensions(t *testing.T) {
	store, err := telemetry.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	turns := []telemetry.TurnMetric{
		{SessionID: "s1", TurnIndex: 0, Model: "gemma3:2b", TotalTokens: 100, DurationMs: 200, CreatedAt: 1000},
		{SessionID: "s1", TurnIndex: 1, Model: "llama3.1", TotalTokens: 50, DurationMs: 400, CreatedAt: 2000},
		{SessionID: "s2", TurnIndex: 0, Model: "gemma3:2b", TotalTokens: 70, DurationMs: 100, CreatedAt: 3000},
	}
	for _, m := range turns {
		if err := store.SaveTurn(ctx, m); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// model-based traces
	byModel, err := store.ByModel(ctx, "")
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	if byModel["gemma3:2b"].Turns != 2 || byModel["gemma3:2b"].TotalTokens != 170 {
		t.Fatalf("byModel gemma = %+v", byModel["gemma3:2b"])
	}
	if byModel["gemma3:2b"].AvgLatencyMs() != 150 {
		t.Fatalf("avg latency = %d", byModel["gemma3:2b"].AvgLatencyMs())
	}

	// session-id map
	bySession, err := store.BySession(ctx)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(bySession) != 2 || bySession["s1"].Turns != 2 {
		t.Fatalf("bySession = %+v", bySession)
	}

	// timestamp + consumption window on totals
	total, err := store.Totals(ctx, "s1")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if total.FirstAt != 1000 || total.LastAt != 2000 || len(total.Models) != 2 {
		t.Fatalf("totals dims = %+v", total)
	}

	// recent traces newest-first, bounded
	recent, err := store.Recent(ctx, "", 2)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 2 || recent[0].CreatedAt != 3000 {
		t.Fatalf("recent = %+v", recent)
	}
	if tr := telemetry.FormatTrace(recent); !strings.Contains(tr, "session=s2") || !strings.Contains(tr, "latency=100ms") {
		t.Fatalf("trace format = %q", tr)
	}
}
