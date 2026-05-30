package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/conformance"
	"github.com/apex-code/apex/internal/provider/ollama"
)

const streamingBody = `{"model":"gemma4:e2b","message":{"role":"assistant","content":"Hello"},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":", world"},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":3}
`

func newOllamaClient(t *testing.T, handler http.HandlerFunc) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ollama.New(ollama.WithBaseURL(srv.URL), ollama.WithModel("gemma4:e2b"), ollama.WithHTTPClient(srv.Client()))
}

func TestOllamaConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T, scenario conformance.Scenario) provider.Provider {
		return newOllamaClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/show":
				io.WriteString(w, `{"details":{"family":"gemma3"},"model_info":{"gemma3.context_length":32768},"capabilities":["completion","tools"]}`)
			case "/api/chat":
				if scenario == conformance.ScenarioToolCall {
					io.WriteString(w, `{"model":"gemma4:e2b","message":{"role":"assistant","tool_calls":[{"function":{"name":"grep","arguments":{"pattern":"TODO"}}}]},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":""},"done":true,"done_reason":"tool_calls"}`)
					return
				}
				io.WriteString(w, streamingBody)
			default:
				t.Fatalf("path = %q", r.URL.Path)
			}
		})
	})
}

func TestOllamaEnsureModelAndCapabilities(t *testing.T) {
	c := newOllamaClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"gemma4:e2b"}]}`)
		case "/api/show":
			io.WriteString(w, `{"details":{"family":"gemma3"},"model_info":{"gemma3.context_length":32768,"gemma3.max_output_tokens":4096},"capabilities":["completion","tools"]}`)
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	})
	if err := c.EnsureModel(context.Background()); err != nil {
		t.Fatalf("EnsureModel: %v", err)
	}
	caps := c.Capabilities()
	if caps.ContextWindow != 32768 || caps.MaxOutputTokens != 4096 {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestOllamaCountTokensFallback(t *testing.T) {
	c := newOllamaClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"details":{"family":"llama"},"model_info":{"llama.context_length":8192},"capabilities":["completion","tools"]}`)
	})

	n, err := c.CountTokens(context.Background(), []domain.Message{{Role: domain.RoleUser, Content: "12345678"}})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if n != 3 {
		t.Fatalf("CountTokens = %d", n)
	}
}

func TestOllamaCompleteModelNotFound(t *testing.T) {
	c := newOllamaClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"model 'ghost' not found"}`)
	})
	_, err := c.Complete(context.Background(), domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "ollama pull") {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaEnsureModelMissing(t *testing.T) {
	c := newOllamaClient(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"models":[{"name":"llama3:latest"}]}`)
	})
	err := c.EnsureModel(context.Background())
	if err == nil || !errors.Is(err, ollama.ErrModelNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaSendsKeepAlive(t *testing.T) {
	var body struct {
		KeepAlive string `json:"keep_alive"`
	}
	c := newOllamaClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		io.WriteString(w, streamingBody)
	})

	stream, err := c.Complete(context.Background(), domain.Request{
		Messages:  []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
		KeepAlive: "10m",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stream.Close()
	if body.KeepAlive != "10m" {
		t.Fatalf("keep_alive = %q", body.KeepAlive)
	}
}
