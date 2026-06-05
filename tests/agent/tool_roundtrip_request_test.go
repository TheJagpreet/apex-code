package agent_test

import (
	"context"
	"testing"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/fake"
)

func TestSecondRequestKeepsAssistantToolCallBeforeToolResult(t *testing.T) {
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
	_, err := agent.New(p, agent.StubToolDispatcher{}).Run(context.Background(), []domain.Message{
		{Role: domain.RoleSystem, Content: "sys"},
		{Role: domain.RoleUser, Content: "find todos"},
	}, agent.Options{MaxIterations: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	requests := p.Requests()
	if len(requests) < 2 {
		t.Fatalf("request count = %d", len(requests))
	}
	second := requests[1].Messages
	if len(second) < 4 {
		t.Fatalf("second request messages = %+v", second)
	}
	assistant := second[len(second)-2]
	tool := second[len(second)-1]
	if assistant.Role != domain.RoleAssistant || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool-call message = %+v", assistant)
	}
	if tool.Role != domain.RoleTool || len(tool.ToolResults) != 1 {
		t.Fatalf("tool result message = %+v", tool)
	}
	if assistant.ToolCalls[0].ID != tool.ToolResults[0].ToolCallID {
		t.Fatalf("tool call id mismatch: assistant=%q tool=%q", assistant.ToolCalls[0].ID, tool.ToolResults[0].ToolCallID)
	}
}

func TestSecondRequestKeepsAssistantToolCallBeforeToolResultsBlock(t *testing.T) {
	p := fake.New(nil).WithScripts([][]provider.StreamEvent{
		{
			{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"README.md"}`)}},
			{Kind: provider.EventDone, StopReason: domain.StopToolUse},
		},
		{
			{Kind: provider.EventText, Text: "done"},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn},
		},
	})
	_, err := agent.New(p, agent.StubToolDispatcher{}).Run(context.Background(), []domain.Message{
		{Role: domain.RoleSystem, Content: "sys"},
		{Role: domain.RoleUser, Content: "read the readme"},
	}, agent.Options{MaxIterations: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	requests := p.Requests()
	if len(requests) < 2 {
		t.Fatalf("request count = %d", len(requests))
	}
	second := requests[1].Messages
	if len(second) < 4 {
		t.Fatalf("second request messages = %+v", second)
	}
	assistant := second[len(second)-2]
	tool := second[len(second)-1]
	if assistant.Role != domain.RoleAssistant || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool-call message = %+v", assistant)
	}
	if tool.Role != domain.RoleTool || len(tool.ToolResults) != 1 {
		t.Fatalf("tool result message = %+v", tool)
	}
}
