package codermode_test

import (
	"context"
	"testing"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/codermode"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/fake"
	"github.com/apex-code/apex/internal/telemetry"
)

func TestCoderTelemetryIncludesExtensionMetadata(t *testing.T) {
	store, err := codermode.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventText, Text: `{"summary":"Review README","phases":[{"name":"review","description":"do it","task_ids":["task-1"]}],"tasks":[{"id":"task-1","phase":"review","title":"Review README","description":"Read README.md and return findings in chat only.","dependencies":[],"status":"pending","owner_agent":"reviewer","acceptance_criteria":["Return findings in chat"],"outputs":["chat table"]}]}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
		{
			{Kind: provider.EventText, Text: `{"status":"done","summary":"Returned review table in chat."}`},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	engine := codermode.NewEngine(p, agent.StubToolDispatcher{}, store, func() agent.Options {
		return agent.Options{}
	})
	engine.SetTelemetryExtensions(func() telemetry.SessionEvent {
		return telemetry.SessionEvent{
			CustomAgent:      "reviewer",
			CustomAgentFile:  "reviewer.md",
			CustomSkills:     []string{"docs"},
			CustomSkillFiles: []string{"docs.md"},
		}
	})
	var events []telemetry.SessionEvent
	engine.SetTelemetrySink(func(_ context.Context, event telemetry.SessionEvent) error {
		events = append(events, event)
		return nil
	})

	wf, err := engine.CreatePlan(context.Background(), "session-1", "review readme")
	if err != nil {
		t.Fatal(err)
	}
	wf, err = engine.ApprovePlan(context.Background(), wf)
	if err != nil {
		t.Fatal(err)
	}
	wf, err = engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatal(err)
	}
	if wf.State != domain.WorkflowStateCompleted {
		t.Fatalf("workflow state = %s, want completed", wf.State)
	}
	if len(events) == 0 {
		t.Fatal("expected telemetry events")
	}
	for _, event := range events {
		if event.Kind != "stage_llm" && event.Kind != "task_llm_turn" {
			continue
		}
		if event.CustomAgent != "reviewer" {
			t.Fatalf("custom agent = %q, want reviewer", event.CustomAgent)
		}
		if len(event.CustomSkills) == 0 || event.CustomSkills[0] != "docs" {
			t.Fatalf("custom skills = %v, want docs", event.CustomSkills)
		}
		return
	}
	t.Fatal("expected at least one llm telemetry event with extension metadata")
}
