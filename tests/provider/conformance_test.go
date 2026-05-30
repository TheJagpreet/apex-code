package provider_test

import (
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/conformance"
	"github.com/apex-code/apex/internal/provider/fake"
)

func TestFakeProviderConformance(t *testing.T) {
	conformance.Run(t, func(_ *testing.T, scenario conformance.Scenario) provider.Provider {
		if scenario == conformance.ScenarioToolCall {
			return fake.New([]provider.StreamEvent{
				{Kind: provider.EventToolCall, ToolCall: &domain.ToolCall{ID: "call_fake_1", Name: "grep", Arguments: []byte(`{"pattern":"TODO"}`)}},
				{Kind: provider.EventDone, StopReason: domain.StopToolUse},
			})
		}
		return fake.New([]provider.StreamEvent{
			{Kind: provider.EventText, Text: "Hello"},
			{Kind: provider.EventText, Text: ", world"},
			{Kind: provider.EventDone, StopReason: domain.StopEndTurn, Usage: &domain.Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}},
		})
	})
}
