package provider_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/conformance"
	"github.com/apex-code/apex/internal/provider/openai"
)

func newOpenAIClient(t *testing.T, handler http.HandlerFunc) *openai.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return openai.New(openai.WithBaseURL(srv.URL), openai.WithHTTPClient(srv.Client()), openai.WithAPIKey("test-key"))
}

func TestOpenAIConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T, scenario conformance.Scenario) provider.Provider {
		return newOpenAIClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chat/completions" {
				t.Fatalf("path = %q", r.URL.Path)
			}
			switch scenario {
			case conformance.ScenarioToolCall:
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"grep\",\"arguments\":\"{\\\"pattern\\\":\\\"TO\"}}]}}]}\n\n")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"DO\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
				io.WriteString(w, "data: [DONE]\n\n")
			default:
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\", world\"},\"finish_reason\":\"\"}]}\n\n")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14}}\n\n")
				io.WriteString(w, "data: [DONE]\n\n")
			}
		})
	})
}

func TestOpenAIHTTPError(t *testing.T) {
	c := newOpenAIClient(t, func(w http.ResponseWriter, _ *http.Request) {
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
