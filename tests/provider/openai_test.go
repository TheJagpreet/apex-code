package provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestOpenAIDeepSeekDisablesThinking(t *testing.T) {
	var body struct {
		Model    string `json:"model"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	serverURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = serverURL.Scheme
			clone.URL.Host = serverURL.Host
			clone.Host = serverURL.Host
			return http.DefaultTransport.RoundTrip(clone)
		}),
	}

	c := openai.New(
		openai.WithBaseURL("https://api.deepseek.com"),
		openai.WithHTTPClient(httpClient),
		openai.WithAPIKey("test-key"),
	)

	_, err = c.Complete(context.Background(), domain.Request{
		Model:    "deepseek-v4-flash",
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if body.Thinking.Type != "disabled" {
		t.Fatalf("thinking.type = %q", body.Thinking.Type)
	}
}

func TestOpenAISynthesizesMissingToolCallID(t *testing.T) {
	c := newOpenAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"type\":\"function\",\"function\":{\"name\":\"grep\",\"arguments\":\"{\\\"pattern\\\":\\\"TODO\\\"}\"}}]}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	stream, err := c.Complete(context.Background(), domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	defer stream.Close()

	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind == provider.EventToolCall {
			if ev.ToolCall == nil || ev.ToolCall.ID != "call_1" {
				t.Fatalf("tool call = %+v", ev.ToolCall)
			}
			return
		}
	}
	t.Fatal("expected tool call event")
}

func TestOpenAISanitizesIncompleteHistoricalToolConversation(t *testing.T) {
	var body struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	c := newOpenAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	stream, err := c.Complete(context.Background(), domain.Request{
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "start"},
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"README.md"}`)},
				{ID: "call_2", Name: "read_file", Arguments: []byte(`{"path":"go.mod"}`)},
			}},
			{Role: domain.RoleTool, ToolResults: []domain.ToolResult{
				{ToolCallID: "call_1", Content: "ok"},
			}},
			{Role: domain.RoleUser, Content: "continue"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stream.Close()

	if len(body.Messages) != 2 {
		t.Fatalf("message count = %d, messages=%+v", len(body.Messages), body.Messages)
	}
	if body.Messages[0].Role != "user" || body.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", body.Messages)
	}
}

func TestOpenAIDropsOrphanHistoricalToolMessages(t *testing.T) {
	var body struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	c := newOpenAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	stream, err := c.Complete(context.Background(), domain.Request{
		Messages: []domain.Message{
			{Role: domain.RoleSystem, Content: "sys"},
			{Role: domain.RoleTool, ToolResults: []domain.ToolResult{{ToolCallID: "call_1", Content: "orphan"}}},
			{Role: domain.RoleUser, Content: "continue"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stream.Close()
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", body.Messages)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	if fn == nil {
		return nil, &net.OpError{Op: "roundtrip", Err: io.EOF}
	}
	return fn(req)
}
