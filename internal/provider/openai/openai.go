package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/sse"
	"github.com/apex-code/apex/internal/tokenizer"
)

const DefaultBaseURL = "https://api.openai.com/v1"

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	caps    provider.Caps
	token   tokenizer.Tokenizer
}

type Option func(*Client)

func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

func WithAPIKey(key string) Option {
	return func(c *Client) {
		c.apiKey = key
	}
}

func WithModel(model string) Option {
	return func(c *Client) {
		if model != "" {
			c.model = model
		}
	}
}

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

func New(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		model:   "gpt-4o-mini",
		http:    &http.Client{},
		caps: provider.Caps{
			ContextWindow:       8192,
			MaxOutputTokens:     4096,
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

func (c *Client) Name() string { return "openai-compatible" }

func (c *Client) CountTokens(_ context.Context, messages []domain.Message) (int, error) {
	return tokenizer.CountMessages(c.token, messages)
}

func (c *Client) Capabilities() provider.Caps { return c.caps }

func (c *Client) Complete(ctx context.Context, req domain.Request) (provider.Stream, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}
	sanitizedMessages := sanitizeToolConversation(req.Messages)
	wire := chatRequest{
		Model:       model,
		Messages:    toMessages(sanitizedMessages),
		Tools:       toTools(req.Tools),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
		Stream:      true,
	}
	if c.isDeepSeek() {
		wire.Thinking = &thinkingConfig{Type: "disabled"}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("openai-compatible: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai-compatible: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai-compatible: POST /chat/completions: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := readErrorBody(resp.Body)
		resp.Body.Close()
		if c.isDeepSeek() && strings.Contains(strings.ToLower(msg), "tool_call") {
			msg += " | request_shape: " + summarizeMessages(wire.Messages)
		}
		return nil, fmt.Errorf("openai-compatible: /chat/completions returned %s: %s", resp.Status, msg)
	}
	return newStream(resp.Body), nil
}

type chatRequest struct {
	Model       string          `json:"model"`
	Messages    []message       `json:"messages"`
	Tools       []tool          `json:"tools,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	Thinking    *thinkingConfig `json:"thinking,omitempty"`
	Stream      bool            `json:"stream"`
}

type thinkingConfig struct {
	Type string `json:"type"`
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type toolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

func toMessages(messages []domain.Message) []message {
	out := make([]message, 0, len(messages))
	for _, m := range messages {
		msg := message{Role: string(m.Role), Content: m.Content}
		if m.Role == domain.RoleTool {
			for _, tr := range m.ToolResults {
				out = append(out, message{
					Role:       "tool",
					Content:    tr.Content,
					ToolCallID: tr.ToolCallID,
				})
			}
			continue
		}
		for _, tc := range m.ToolCalls {
			wire := toolCall{ID: tc.ID, Type: "function"}
			wire.Function.Name = tc.Name
			wire.Function.Arguments = string(tc.Arguments)
			msg.ToolCalls = append(msg.ToolCalls, wire)
		}
		out = append(out, msg)
	}
	return out
}

func toTools(specs []domain.ToolSpec) []tool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]tool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, tool{
			Type: "function",
			Function: toolFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
		})
	}
	return out
}

type stream struct {
	rc        io.ReadCloser
	reader    *sse.Reader
	pending   []provider.StreamEvent
	toolCalls map[int]*domain.ToolCall
}

func newStream(rc io.ReadCloser) *stream {
	return &stream{
		rc:        rc,
		reader:    sse.NewReader(rc),
		toolCalls: map[int]*domain.ToolCall{},
	}
}

func (s *stream) Recv() (provider.StreamEvent, error) {
	if len(s.pending) > 0 {
		ev := s.pending[0]
		s.pending = s.pending[1:]
		return ev, nil
	}

	for {
		ev, err := s.reader.Next()
		if err != nil {
			return provider.StreamEvent{}, err
		}
		if sse.IsDoneMarker(ev.Data) {
			return provider.StreamEvent{}, io.EOF
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(ev.Data, &chunk); err != nil {
			return provider.StreamEvent{}, fmt.Errorf("openai-compatible: decode chunk: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			return provider.StreamEvent{Kind: provider.EventText, Text: choice.Delta.Content}, nil
		}
		for _, tc := range choice.Delta.ToolCalls {
			call := s.toolCalls[tc.Index]
			if call == nil {
				call = &domain.ToolCall{}
				s.toolCalls[tc.Index] = call
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Function.Name != "" {
				call.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				call.Arguments = append(call.Arguments, tc.Function.Arguments...)
			}
		}
		if choice.FinishReason != "" {
			if choice.FinishReason == "tool_calls" {
				for i := 0; i < len(s.toolCalls); i++ {
					if call := s.toolCalls[i]; call != nil {
						ensureToolCallID(call, i)
						copied := *call
						s.pending = append(s.pending, provider.StreamEvent{
							Kind:     provider.EventToolCall,
							ToolCall: &copied,
						})
					}
				}
			}
			s.pending = append(s.pending, provider.StreamEvent{
				Kind:       provider.EventDone,
				StopReason: mapStopReason(choice.FinishReason),
				Usage: &domain.Usage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
				},
			})
			return s.Recv()
		}
	}
}

func (s *stream) Close() error {
	if s.rc == nil {
		return nil
	}
	return s.rc.Close()
}

func mapStopReason(reason string) domain.StopReason {
	switch reason {
	case "tool_calls":
		return domain.StopToolUse
	case "length":
		return domain.StopMaxTokens
	case "stop":
		return domain.StopEndTurn
	case "content_filter":
		return domain.StopStop
	default:
		return domain.StopUnknown
	}
}

func readErrorBody(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 8<<10))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(raw))
}

func (c *Client) isDeepSeek() bool {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return strings.Contains(strings.ToLower(c.baseURL), "deepseek.com")
	}
	return strings.Contains(strings.ToLower(u.Host), "deepseek.com")
}

func ensureToolCallID(call *domain.ToolCall, index int) {
	if call == nil || strings.TrimSpace(call.ID) != "" {
		return
	}
	call.ID = "call_" + strconv.Itoa(index+1)
}

func sanitizeToolConversation(messages []domain.Message) []domain.Message {
	out := make([]domain.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role == domain.RoleTool {
			continue
		}
		if msg.Role != domain.RoleAssistant || len(msg.ToolCalls) == 0 {
			out = append(out, msg)
			continue
		}

		j := i + 1
		seen := map[string]bool{}
		for ; j < len(messages) && messages[j].Role == domain.RoleTool; j++ {
			for _, tr := range messages[j].ToolResults {
				if strings.TrimSpace(tr.ToolCallID) != "" {
					seen[strings.TrimSpace(tr.ToolCallID)] = true
				}
			}
		}

		valid := true
		for idx := range msg.ToolCalls {
			call := msg.ToolCalls[idx]
			ensureToolCallID(&call, idx)
			if !seen[call.ID] {
				valid = false
				break
			}
		}
		if !valid {
			i = j - 1
			continue
		}
		out = append(out, msg)
		for k := i + 1; k < j; k++ {
			out = append(out, messages[k])
		}
		i = j - 1
	}
	return out
}

func summarizeMessages(messages []message) string {
	parts := make([]string, 0, len(messages))
	for i, msg := range messages {
		part := fmt.Sprintf("%d:%s", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			ids := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				ids = append(ids, firstNonEmpty(tc.ID, tc.Function.Name))
			}
			part += "[tool_calls=" + strings.Join(ids, ",") + "]"
		}
		if msg.ToolCallID != "" {
			part += "[tool_call_id=" + msg.ToolCallID + "]"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " -> ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var _ provider.Provider = (*Client)(nil)
