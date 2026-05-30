package conformance

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
)

type Scenario string

const (
	ScenarioText     Scenario = "text"
	ScenarioToolCall Scenario = "tool_call"
)

type Factory func(t *testing.T, scenario Scenario) provider.Provider

func Run(t *testing.T, factory Factory) {
	t.Helper()

	tests := []struct {
		name     string
		scenario Scenario
		request  domain.Request
		assert   func(t *testing.T, got collected)
	}{
		{
			name:     "text stream reaches done",
			scenario: ScenarioText,
			request: domain.Request{
				Messages: []domain.Message{{Role: domain.RoleUser, Content: "hello"}},
			},
			assert: func(t *testing.T, got collected) {
				t.Helper()
				if got.text != "Hello, world" {
					t.Fatalf("text = %q, want %q", got.text, "Hello, world")
				}
				if got.stop != domain.StopEndTurn {
					t.Fatalf("stop = %q, want %q", got.stop, domain.StopEndTurn)
				}
			},
		},
		{
			name:     "tool call is normalized",
			scenario: ScenarioToolCall,
			request: domain.Request{
				Messages: []domain.Message{{Role: domain.RoleUser, Content: "find todos"}},
			},
			assert: func(t *testing.T, got collected) {
				t.Helper()
				if len(got.toolCalls) != 1 {
					t.Fatalf("tool calls = %d, want 1", len(got.toolCalls))
				}
				if got.toolCalls[0].Name != "grep" {
					t.Fatalf("tool name = %q, want grep", got.toolCalls[0].Name)
				}
				if !strings.Contains(string(got.toolCalls[0].Arguments), "TODO") {
					t.Fatalf("tool arguments = %q, want TODO", got.toolCalls[0].Arguments)
				}
				if got.stop != domain.StopToolUse {
					t.Fatalf("stop = %q, want %q", got.stop, domain.StopToolUse)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := factory(t, tc.scenario)
			if p.Name() == "" {
				t.Fatal("provider name must not be empty")
			}

			n, err := p.CountTokens(context.Background(), tc.request.Messages)
			if err != nil {
				t.Fatalf("CountTokens: %v", err)
			}
			if n <= 0 {
				t.Fatalf("CountTokens = %d, want > 0", n)
			}

			caps := p.Capabilities()
			if !caps.SupportsStreaming {
				t.Fatalf("caps = %+v, want SupportsStreaming", caps)
			}

			stream, err := p.Complete(context.Background(), tc.request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			defer stream.Close()

			tc.assert(t, collect(t, stream))
		})
	}
}

type collected struct {
	text      string
	toolCalls []domain.ToolCall
	stop      domain.StopReason
}

func collect(t *testing.T, stream provider.Stream) collected {
	t.Helper()
	var out collected

	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}

		switch ev.Kind {
		case provider.EventText:
			out.text += ev.Text
		case provider.EventToolCall:
			if ev.ToolCall != nil {
				out.toolCalls = append(out.toolCalls, *ev.ToolCall)
			}
		case provider.EventDone:
			out.stop = ev.StopReason
		}
	}

	return out
}
