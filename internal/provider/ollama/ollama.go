// Package ollama implements the provider.Provider interface against a local
// Ollama server (https://github.com/ollama/ollama), using the streaming
// /api/chat endpoint. It is apex-code's default backend.
//
// Default model is gemma4:e2b (plan step 1.6). Capability auto-detection via
// /api/show and a Gemma-aware tokenizer are implemented here; unknown models
// still fall back to conservative defaults and heuristic counting.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/tokenizer"
)

// DefaultBaseURL is where a local Ollama server listens by default.
const DefaultBaseURL = "http://localhost:11434"

// DefaultModel is apex-code's default local model (plan step 1.6).
const DefaultModel = "gemma4:e2b"

// defaultContextWindow is used until /api/show introspection lands (step 1.7).
const defaultContextWindow = 8192

// Client is an Ollama-backed provider.Provider. The zero value is not usable;
// construct it with New.
type Client struct {
	baseURL string
	model   string
	http    *http.Client

	mu    sync.RWMutex
	caps  provider.Caps
	token tokenizer.Tokenizer
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the Ollama server address.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithModel overrides the model tag (e.g. "gemma4:e2b", "llama3.1").
func WithModel(m string) Option {
	return func(c *Client) {
		if m != "" {
			c.model = m
		}
	}
}

// WithHTTPClient injects a custom *http.Client (timeouts, transport, tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// New builds a Client with sane defaults, applying any options.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		model:   DefaultModel,
		// No top-level timeout: streaming completions can run long. Callers
		// control duration via context cancellation.
		http: &http.Client{},
		caps: provider.Caps{
			ContextWindow:       defaultContextWindow,
			MaxOutputTokens:     0,
			SupportsTools:       true,
			SupportsStreaming:   true,
			SupportsPromptCache: false,
		},
		token: tokenizer.NewHeuristic(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Model reports the model tag this client will use by default.
func (c *Client) Model() string { return c.model }

// ErrModelNotInstalled is returned (wrapped) by EnsureModel when the configured
// model is not present on the Ollama server. Callers can match it with
// errors.Is to distinguish a missing model from a connection failure.
var ErrModelNotInstalled = errors.New("ollama: model not installed")

// EnsureModel proactively verifies the configured model is present on the
// server by querying /api/tags, returning a clear, actionable error if it is
// not. Run it once at startup so users get a "run `ollama pull ...`" hint up
// front rather than a mid-completion 404.
func (c *Client) EnsureModel(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama: build /api/tags request: %w", err)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ollama: GET /api/tags: %w (is `ollama serve` running at %s?)", err, c.baseURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: /api/tags returned %s: %s", resp.Status, readErrorBody(resp.Body))
	}

	var tags tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return fmt.Errorf("ollama: decode /api/tags: %w", err)
	}

	for _, m := range tags.Models {
		if modelNameMatches(m.Name, c.model) {
			_ = c.refreshModelMetadata(ctx, c.model)
			return nil
		}
	}

	return fmt.Errorf("%w: %q — run `ollama pull %s`", ErrModelNotInstalled, c.model, c.model)
}

// modelNameMatches compares an installed model tag against the wanted tag,
// tolerating Ollama's implicit ":latest" suffix (e.g. "gemma4:e2b" matches an
// installed "gemma4:e2b", and "llama3" matches an installed "llama3:latest").
func modelNameMatches(installed, want string) bool {
	if installed == want {
		return true
	}
	return withLatest(installed) == withLatest(want)
}

func withLatest(tag string) string {
	if strings.Contains(tag, ":") {
		return tag
	}
	return tag + ":latest"
}

// tagsResponse is the shape of GET /api/tags.
type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

type showRequest struct {
	Model   string `json:"model"`
	Verbose bool   `json:"verbose,omitempty"`
}

type showResponse struct {
	Details struct {
		Family   string   `json:"family"`
		Families []string `json:"families"`
	} `json:"details"`
	ModelInfo    map[string]any `json:"model_info"`
	Capabilities []string       `json:"capabilities"`
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "ollama" }

// Capabilities implements provider.Provider.
//
// Values are conservative defaults; /api/show-based detection (step 1.7) will
// populate ContextWindow/MaxOutputTokens per actual model.
func (c *Client) Capabilities() provider.Caps {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps
}

// CountTokens implements provider.Provider. It prefers the model-aware
// tokenizer learned from /api/show and falls back to a clearly heuristic
// counter when the model does not expose tokenizer metadata.
func (c *Client) CountTokens(ctx context.Context, messages []domain.Message) (int, error) {
	c.mu.RLock()
	tok := c.token
	c.mu.RUnlock()

	if !tok.Exact() {
		if err := c.refreshModelMetadata(ctx, c.model); err == nil {
			c.mu.RLock()
			tok = c.token
			c.mu.RUnlock()
		}
	}

	return tokenizer.CountMessages(tok, messages)
}

// --- wire types for /api/chat ---

type chatRequest struct {
	Model     string         `json:"model"`
	Messages  []chatMessage  `json:"messages"`
	Stream    bool           `json:"stream"`
	Tools     []chatTool     `json:"tools,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
}

type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"` // always "function"
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// chatChunk is one NDJSON line from a streaming /api/chat response.
type chatChunk struct {
	Model      string      `json:"model"`
	CreatedAt  time.Time   `json:"created_at"`
	Message    chatMessage `json:"message"`
	Done       bool        `json:"done"`
	DoneReason string      `json:"done_reason"`

	// Reported once on the final (done) chunk.
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

// Complete implements provider.Provider.
func (c *Client) Complete(ctx context.Context, req domain.Request) (provider.Stream, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}

	wire := chatRequest{
		Model:     model,
		Messages:  toChatMessages(req.Messages),
		Stream:    true,
		Tools:     toChatTools(req.Tools),
		Options:   toOptions(req),
		KeepAlive: req.KeepAlive,
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: POST /api/chat: %w (is `ollama serve` running at %s?)", err, c.baseURL)
	}

	if resp.StatusCode != http.StatusOK {
		msg := readErrorBody(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("ollama: model %q not found (try `ollama pull %s`): %s", model, model, msg)
		}
		return nil, fmt.Errorf("ollama: /api/chat returned %s: %s", resp.Status, msg)
	}

	return newStream(resp.Body), nil
}

// readErrorBody extracts Ollama's {"error":"..."} message, falling back to raw.
func readErrorBody(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 8<<10))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(raw))
}

func (c *Client) refreshModelMetadata(ctx context.Context, model string) error {
	body, err := json.Marshal(showRequest{Model: model, Verbose: true})
	if err != nil {
		return fmt.Errorf("ollama: marshal /api/show request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/show", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama: build /api/show request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ollama: POST /api/show: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: /api/show returned %s: %s", resp.Status, readErrorBody(resp.Body))
	}

	var show showResponse
	if err := json.NewDecoder(resp.Body).Decode(&show); err != nil {
		return fmt.Errorf("ollama: decode /api/show: %w", err)
	}

	caps := provider.Caps{
		ContextWindow:       defaultContextWindow,
		MaxOutputTokens:     0,
		SupportsTools:       true,
		SupportsStreaming:   true,
		SupportsPromptCache: false,
	}

	if n, ok := detectContextWindow(show.ModelInfo); ok && n > 0 {
		caps.ContextWindow = n
	}
	if n, ok := detectMaxOutputTokens(show.ModelInfo); ok && n > 0 {
		caps.MaxOutputTokens = n
	}
	if len(show.Capabilities) > 0 {
		caps.SupportsTools = false
		for _, capability := range show.Capabilities {
			if capability == "tools" {
				caps.SupportsTools = true
				break
			}
		}
	}

	tok := tokenizer.Tokenizer(tokenizer.NewHeuristic())
	if spTok, err := buildTokenizerFromShow(model, show); err == nil {
		tok = spTok
	}

	c.mu.Lock()
	c.caps = caps
	c.token = tok
	c.mu.Unlock()

	return nil
}

func buildTokenizerFromShow(model string, show showResponse) (tokenizer.Tokenizer, error) {
	if !looksSentencePiece(show, model) {
		return nil, fmt.Errorf("unsupported tokenizer family")
	}

	pieces, ok := stringSlice(show.ModelInfo["tokenizer.ggml.tokens"])
	if !ok {
		return nil, fmt.Errorf("missing tokenizer.ggml.tokens")
	}
	scores, _ := float64Slice(show.ModelInfo["tokenizer.ggml.scores"])
	return tokenizer.NewSentencePiece("ollama-"+model, pieces, scores)
}

func looksSentencePiece(show showResponse, model string) bool {
	if familyLooksGemma(show.Details.Family) {
		return true
	}
	for _, family := range show.Details.Families {
		if familyLooksGemma(family) {
			return true
		}
	}
	if arch, _ := show.ModelInfo["general.architecture"].(string); familyLooksGemma(arch) {
		return true
	}
	return familyLooksGemma(model)
}

func familyLooksGemma(s string) bool {
	return strings.Contains(strings.ToLower(s), "gemma")
}

func detectContextWindow(info map[string]any) (int, bool) {
	for k, v := range info {
		key := strings.ToLower(k)
		if strings.HasSuffix(key, ".context_length") || strings.HasSuffix(key, ".context_window") {
			if n, ok := intValue(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func detectMaxOutputTokens(info map[string]any) (int, bool) {
	for k, v := range info {
		key := strings.ToLower(k)
		if strings.HasSuffix(key, ".max_output_tokens") || strings.HasSuffix(key, ".max_generation_tokens") {
			if n, ok := intValue(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		var i int
		_, err := fmt.Sscanf(n, "%d", &i)
		return i, err == nil
	default:
		return 0, false
	}
}

func stringSlice(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func float64Slice(v any) ([]float64, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]float64, 0, len(items))
	for _, item := range items {
		f, ok := item.(float64)
		if !ok {
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

func toOptions(req domain.Request) map[string]any {
	opts := map[string]any{}
	if req.Temperature != 0 {
		opts["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

func toChatMessages(msgs []domain.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		// Tool results map to one Ollama "tool" message each.
		if m.Role == domain.RoleTool {
			for _, tr := range m.ToolResults {
				out = append(out, chatMessage{Role: "tool", Content: tr.Content})
			}
			if len(m.ToolResults) == 0 && m.Content != "" {
				out = append(out, chatMessage{Role: "tool", Content: m.Content})
			}
			continue
		}

		cm := chatMessage{Role: string(m.Role), Content: m.Content}
		for _, tc := range m.ToolCalls {
			var ctc chatToolCall
			ctc.Function.Name = tc.Name
			ctc.Function.Arguments = tc.Arguments
			cm.ToolCalls = append(cm.ToolCalls, ctc)
		}
		out = append(out, cm)
	}
	return out
}

func toChatTools(tools []domain.ToolSpec) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

// --- streaming decode ---

type stream struct {
	rc      io.ReadCloser
	scanner *bufio.Scanner
	done    bool
}

func newStream(rc io.ReadCloser) *stream {
	sc := bufio.NewScanner(rc)
	// NDJSON lines can be large (tool-call arguments); grow the buffer.
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	return &stream{rc: rc, scanner: sc}
}

// Recv implements provider.Stream.
func (s *stream) Recv() (provider.StreamEvent, error) {
	if s.done {
		return provider.StreamEvent{}, io.EOF
	}

	for s.scanner.Scan() {
		line := bytes.TrimSpace(s.scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var chunk chatChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			return provider.StreamEvent{}, fmt.Errorf("ollama: decode chunk: %w", err)
		}

		// A chunk may carry a tool call, text, or both. Prefer surfacing tool
		// calls as their own event(s).
		if len(chunk.Message.ToolCalls) > 0 {
			tc := chunk.Message.ToolCalls[0]
			return provider.StreamEvent{
				Kind: provider.EventToolCall,
				ToolCall: &domain.ToolCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}, nil
		}

		if chunk.Message.Content != "" {
			return provider.StreamEvent{Kind: provider.EventText, Text: chunk.Message.Content}, nil
		}

		if chunk.Done {
			s.done = true
			// Emit usage first, then the caller will Recv again to get Done.
			return provider.StreamEvent{
				Kind: provider.EventDone,
				Usage: &domain.Usage{
					PromptTokens:     chunk.PromptEvalCount,
					CompletionTokens: chunk.EvalCount,
					TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
				},
				StopReason: mapStopReason(chunk),
			}, nil
		}
		// Empty, non-done chunk (e.g. role-only opener): skip.
	}

	if err := s.scanner.Err(); err != nil {
		return provider.StreamEvent{}, fmt.Errorf("ollama: read stream: %w", err)
	}
	// Scanner exhausted without an explicit done chunk.
	s.done = true
	return provider.StreamEvent{}, io.EOF
}

// Close implements provider.Stream.
func (s *stream) Close() error {
	s.done = true
	if s.rc == nil {
		return nil
	}
	return s.rc.Close()
}

func mapStopReason(chunk chatChunk) domain.StopReason {
	if len(chunk.Message.ToolCalls) > 0 {
		return domain.StopToolUse
	}
	switch chunk.DoneReason {
	case "stop":
		return domain.StopEndTurn
	case "length":
		return domain.StopMaxTokens
	case "tool_calls":
		return domain.StopToolUse
	default:
		return domain.StopEndTurn
	}
}

// compile-time assertion that Client satisfies the interface.
var _ provider.Provider = (*Client)(nil)
