package provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/anthropic"
	"github.com/apex-code/apex/internal/provider/conformance"
)

func newAnthropicClient(t *testing.T, handler http.HandlerFunc) *anthropic.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithHTTPClient(srv.Client()), anthropic.WithAPIKey("test-key"))
}

func TestAnthropicConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T, scenario conformance.Scenario) provider.Provider {
		return newAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/messages" {
				t.Fatalf("path = %q", r.URL.Path)
			}
			switch scenario {
			case conformance.ScenarioToolCall:
				io.WriteString(w, "event: content_block_start\n")
				io.WriteString(w, "data: {\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"grep\",\"input\":{\"pattern\":\"TODO\"}}}\n\n")
				io.WriteString(w, "event: content_block_stop\n")
				io.WriteString(w, "data: {}\n\n")
				io.WriteString(w, "event: message_delta\n")
				io.WriteString(w, "data: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":4}}\n\n")
				io.WriteString(w, "event: message_stop\n")
				io.WriteString(w, "data: {}\n\n")
			default:
				io.WriteString(w, "event: content_block_start\n")
				io.WriteString(w, "data: {\"content_block\":{\"type\":\"text\",\"text\":\"Hello\"}}\n\n")
				io.WriteString(w, "event: content_block_delta\n")
				io.WriteString(w, "data: {\"delta\":{\"type\":\"text_delta\",\"text\":\", world\"}}\n\n")
				io.WriteString(w, "event: message_delta\n")
				io.WriteString(w, "data: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n")
				io.WriteString(w, "event: message_stop\n")
				io.WriteString(w, "data: {}\n\n")
			}
		})
	})
}

func TestAnthropicHTTPError(t *testing.T) {
	c := newAnthropicClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"bad request"}}`)
	})
	_, err := c.Complete(context.Background(), domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAnthropicPromptCacheBreakpoint(t *testing.T) {
	var body struct {
		System []struct {
			Type         string `json:"type"`
			Text         string `json:"text"`
			CacheControl struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"system"`
	}
	c := newAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		io.WriteString(w, "event: content_block_start\n")
		io.WriteString(w, "data: {\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\n")
		io.WriteString(w, "event: message_delta\n")
		io.WriteString(w, "data: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		io.WriteString(w, "event: message_stop\n")
		io.WriteString(w, "data: {}\n\n")
	})

	stream, err := c.Complete(context.Background(), domain.Request{
		Messages: []domain.Message{
			{Role: domain.RoleSystem, Content: "stable", CacheControl: "ephemeral"},
			{Role: domain.RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stream.Close()
	if len(body.System) != 1 || body.System[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("system = %+v", body.System)
	}
}

func TestAnthropicCacheUsageReporting(t *testing.T) {
	c := newAnthropicClient(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "event: message_start\n")
		io.WriteString(w, "data: {\"message\":{\"usage\":{\"input_tokens\":20,\"cache_creation_input_tokens\":5,\"cache_read_input_tokens\":7}}}\n\n")
		io.WriteString(w, "event: content_block_start\n")
		io.WriteString(w, "data: {\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\n")
		io.WriteString(w, "event: message_delta\n")
		io.WriteString(w, "data: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n")
		io.WriteString(w, "event: message_stop\n")
		io.WriteString(w, "data: {}\n\n")
	})
	stream, err := c.Complete(context.Background(), domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	defer stream.Close()

	var usage domain.Usage
	for {
		ev, err := stream.Recv()
		if err != nil {
			break
		}
		if ev.Usage != nil {
			usage = *ev.Usage
		}
	}
	if usage.PromptTokens != 20 || usage.CompletionTokens != 3 || usage.CacheCreationTokens != 5 || usage.CacheReadTokens != 7 {
		t.Fatalf("usage = %+v", usage)
	}
}
