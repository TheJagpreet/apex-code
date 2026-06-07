package telemetry_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apex-code/apex/internal/domain"
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

func TestSessionFileStoreAppendsStructuredTelemetry(t *testing.T) {
	store, err := telemetry.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	ctx := context.Background()
	err = store.AppendEvent(ctx, "sess-1", telemetry.FileMeta{Model: "deepseek-v4-flash", CWD: "E:/repo"}, telemetry.SessionEvent{
		Index:            1,
		Timestamp:        time.Unix(1000, 0).UTC(),
		Mode:             "chat",
		Kind:             "llm_turn",
		Model:            "deepseek-v4-flash",
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		ToolCalls:        []string{"read_file"},
		ToolCallDetails: []domain.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)},
		},
		ToolResults: 1,
		ToolResultDetails: []domain.ToolResult{
			{ToolCallID: "call_1", Content: "README contents"},
		},
		InputMessages: []domain.Message{
			{Role: domain.RoleUser, Content: "Read the README"},
		},
		OutputMessage: &domain.Message{
			Role:    domain.RoleAssistant,
			Content: "",
			ToolCalls: []domain.ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)},
			},
		},
	})
	if err != nil {
		t.Fatalf("append event 1: %v", err)
	}
	err = store.AppendEvent(ctx, "sess-1", telemetry.FileMeta{}, telemetry.SessionEvent{
		Index:            2,
		Timestamp:        time.Unix(1001, 0).UTC(),
		Mode:             "coder",
		Kind:             "task_llm_turn",
		Model:            "deepseek-v4-flash",
		PromptTokens:     20,
		CompletionTokens: 10,
		TotalTokens:      30,
		WorkflowID:       "wf-1",
		TaskID:           "T1",
		Agent:            "solutioner",
		CustomAgent:      "frontend",
		CustomAgentFile:  "frontend.md",
		CustomSkills:     []string{"tailwind"},
		CustomSkillFiles: []string{"tailwind.md"},
	})
	if err != nil {
		t.Fatalf("append event 2: %v", err)
	}
	totals, count, err := store.SessionTotals(ctx, "sess-1")
	if err != nil {
		t.Fatalf("session totals: %v", err)
	}
	if count != 2 || totals.TotalTokens != 45 {
		t.Fatalf("totals=%+v count=%d", totals, count)
	}
	path := store.TelemetryPath("sess-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry file: %v", err)
	}
	if !strings.Contains(string(data), `"session_id": "sess-1"`) ||
		!strings.Contains(string(data), `"workflow_id": "wf-1"`) ||
		!strings.Contains(string(data), `"custom_agent": "frontend"`) ||
		!strings.Contains(string(data), `"custom_skill_files"`) ||
		!strings.Contains(string(data), `"tool_call_details"`) ||
		!strings.Contains(string(data), `"input_messages"`) ||
		!strings.Contains(string(data), `"output_message"`) ||
		!strings.Contains(string(data), `"path": "README.md"`) {
		t.Fatalf("telemetry file missing expected fields: %s", string(data))
	}
	if filepath.Base(filepath.Dir(path)) != "sess-1" {
		t.Fatalf("telemetry path should live in session dir, got %s", path)
	}
}

func TestToolExecOutcomeClassifiesRecoverableErrors(t *testing.T) {
	outcome, recoverable := telemetry.ToolExecOutcome([]domain.ToolResult{
		{
			ToolCallID: "call_1",
			Content:    "tool error: file too large for full read; provide start_line and end_line",
			IsError:    true,
		},
	})
	if outcome != "recoverable_error" || !recoverable {
		t.Fatalf("expected recoverable tool error, got outcome=%q recoverable=%v", outcome, recoverable)
	}

	outcome, recoverable = telemetry.ToolExecOutcome([]domain.ToolResult{
		{
			ToolCallID: "call_2",
			Content:    "tool error: permission denied",
			IsError:    true,
		},
	})
	if outcome != "error" || recoverable {
		t.Fatalf("expected terminal tool error, got outcome=%q recoverable=%v", outcome, recoverable)
	}

	outcome, recoverable = telemetry.ToolExecOutcome([]domain.ToolResult{
		{
			ToolCallID: "call_3",
			Content:    "ok",
		},
	})
	if outcome != "success" || recoverable {
		t.Fatalf("expected success, got outcome=%q recoverable=%v", outcome, recoverable)
	}
}

func TestTelemetryRollupsIgnoreNonLLMEvents(t *testing.T) {
	root := t.TempDir()
	files, err := telemetry.OpenFileStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	ctx := context.Background()
	if err := files.AppendEvent(ctx, "sess-1", telemetry.FileMeta{Model: "deepseek-v4-flash"}, telemetry.SessionEvent{
		Index:            1,
		Timestamp:        time.Unix(1000, 0).UTC(),
		Kind:             "llm_turn",
		Model:            "deepseek-v4-flash",
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}); err != nil {
		t.Fatalf("append llm event: %v", err)
	}
	if err := files.AppendEvent(ctx, "sess-1", telemetry.FileMeta{}, telemetry.SessionEvent{
		Index:       2,
		Timestamp:   time.Unix(1001, 0).UTC(),
		Kind:        "tool_exec",
		ToolCalls:   []string{"list_dir"},
		ToolResults: 1,
	}); err != nil {
		t.Fatalf("append tool event: %v", err)
	}
	store, err := telemetry.Open(root)
	if err != nil {
		t.Fatalf("open telemetry store: %v", err)
	}
	defer store.Close()
	totals, err := store.Totals(ctx, "sess-1")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.Turns != 1 || totals.TotalTokens != 15 {
		t.Fatalf("totals should ignore non-llm events: %+v", totals)
	}
	recent, err := store.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 1 || recent[0].TotalTokens != 15 {
		t.Fatalf("recent should ignore non-llm events: %+v", recent)
	}
}

func TestTelemetryOpenUsesDotPrefixedDataDirAsDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".apex")
	files, err := telemetry.OpenFileStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	if err := files.AppendEvent(context.Background(), "sess-1", telemetry.FileMeta{Model: "deepseek-v4-flash"}, telemetry.SessionEvent{
		Index:       1,
		Timestamp:   time.Unix(1000, 0).UTC(),
		Kind:        "llm_turn",
		Model:       "deepseek-v4-flash",
		TotalTokens: 15,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	store, err := telemetry.Open(root)
	if err != nil {
		t.Fatalf("open telemetry store: %v", err)
	}
	defer store.Close()
	totals, err := store.Totals(context.Background(), "")
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.TotalTokens != 15 || totals.Turns != 1 {
		t.Fatalf("unexpected totals: %+v", totals)
	}
}
