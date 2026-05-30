package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/sse"
	"github.com/apex-code/apex/internal/tokenizer"
)

const (
	DefaultBaseURL = "https://api.anthropic.com"
	DefaultVersion = "2023-06-01"
)

type Client struct {
	baseURL string
	apiKey  string
	version string
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

func WithVersion(version string) Option {
	return func(c *Client) {
		if version != "" {
			c.version = version
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
		version: DefaultVersion,
		model:   "claude-3-5-sonnet-latest",
		http:    &http.Client{},
		caps: provider.Caps{
			ContextWindow:       8192,
			MaxOutputTokens:     4096,
			SupportsTools:       true,
			SupportsStreaming:   true,
			SupportsPromptCache: true,
		},
		token: tokenizer.NewHeuristic(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) Name() string { return "anthropic" }

func (c *Client) Capabilities() provider.Caps { return c.caps }

func (c *Client) CountTokens(_ context.Context, messages []domain.Message) (int, error) {
	return tokenizer.CountMessages(c.token, messages)
}

func (c *Client) Complete(ctx context.Context, req domain.Request) (provider.Stream, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}

	wire := messageRequest{
		Model:       model,
		System:      extractSystem(req.Messages),
		Messages:    toMessages(req.Messages),
		Tools:       toTools(req.Tools),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		StopSeqs:    req.Stop,
		Stream:      true,
	}
	if wire.MaxTokens == 0 {
		wire.MaxTokens = c.caps.MaxOutputTokens
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", c.version)
	if c.apiKey != "" {
		httpReq.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: POST /v1/messages: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := readErrorBody(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic: /v1/messages returned %s: %s", resp.Status, msg)
	}
	return newStream(resp.Body), nil
}

type messageRequest struct {
	Model       string            `json:"model"`
	System      any               `json:"system,omitempty"`
	Messages    []message         `json:"messages"`
	Tools       []tool            `json:"tools,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens"`
	StopSeqs    []string          `json:"stop_sequences,omitempty"`
	Stream      bool              `json:"stream"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

func extractSystem(messages []domain.Message) any {
	var blocks []contentBlock
	hasCache := false
	for _, m := range messages {
		if m.Role == domain.RoleSystem && m.Content != "" {
			block := contentBlock{Type: "text", Text: m.Content}
			if m.CacheControl != "" {
				block.CacheControl = &cacheControl{Type: m.CacheControl}
				hasCache = true
			}
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	if !hasCache {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			parts = append(parts, block.Text)
		}
		return strings.Join(parts, "\n\n")
	}
	return blocks
}

func toMessages(messages []domain.Message) []message {
	out := make([]message, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case domain.RoleSystem:
			continue
		case domain.RoleTool:
			var blocks []contentBlock
			for _, tr := range m.ToolResults {
				payload := tr.Content
				if payload == "" {
					payload = m.Content
				}
				blocks = append(blocks, contentBlock{Type: "text", Text: payload})
			}
			if len(blocks) > 0 {
				out = append(out, message{Role: "user", Content: blocks})
			}
		default:
			msg := message{Role: string(m.Role)}
			if m.Content != "" {
				block := contentBlock{Type: "text", Text: m.Content}
				if m.CacheControl != "" {
					block.CacheControl = &cacheControl{Type: m.CacheControl}
				}
				msg.Content = append(msg.Content, block)
			}
			for _, tc := range m.ToolCalls {
				msg.Content = append(msg.Content, contentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Arguments,
				})
			}
			if len(msg.Content) > 0 {
				out = append(out, msg)
			}
		}
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
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.Parameters,
		})
	}
	return out
}

type stream struct {
	rc      io.ReadCloser
	reader  *sse.Reader
	pending []provider.StreamEvent
	tool    *domain.ToolCall
	usage   domain.Usage
}

func newStream(rc io.ReadCloser) *stream {
	return &stream{rc: rc, reader: sse.NewReader(rc)}
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
		if len(ev.Data) == 0 {
			continue
		}

		switch ev.Type {
		case "message_start":
			var start struct {
				Message struct {
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal(ev.Data, &start); err != nil {
				return provider.StreamEvent{}, fmt.Errorf("anthropic: decode message_start: %w", err)
			}
			s.usage.PromptTokens = start.Message.Usage.InputTokens
			s.usage.CacheCreationTokens = start.Message.Usage.CacheCreationInputTokens
			s.usage.CacheReadTokens = start.Message.Usage.CacheReadInputTokens
			if s.usage.PromptTokens > 0 || s.usage.CacheCreationTokens > 0 || s.usage.CacheReadTokens > 0 {
				usage := s.usage
				return provider.StreamEvent{Kind: provider.EventUsage, Usage: &usage}, nil
			}

		case "content_block_delta":
			var delta struct {
				Delta struct {
					Type         string `json:"type"`
					Text         string `json:"text"`
					PartialJSON  string `json:"partial_json"`
					PartialInput string `json:"partial_input"`
				} `json:"delta"`
			}
			if err := json.Unmarshal(ev.Data, &delta); err != nil {
				return provider.StreamEvent{}, fmt.Errorf("anthropic: decode content_block_delta: %w", err)
			}
			if delta.Delta.Text != "" {
				return provider.StreamEvent{Kind: provider.EventText, Text: delta.Delta.Text}, nil
			}
			if s.tool != nil {
				s.tool.Arguments = append(s.tool.Arguments, []byte(delta.Delta.PartialJSON+delta.Delta.PartialInput)...)
			}

		case "content_block_start":
			var start struct {
				ContentBlock struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
					Text  string          `json:"text"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal(ev.Data, &start); err != nil {
				return provider.StreamEvent{}, fmt.Errorf("anthropic: decode content_block_start: %w", err)
			}
			switch start.ContentBlock.Type {
			case "text":
				if start.ContentBlock.Text != "" {
					return provider.StreamEvent{Kind: provider.EventText, Text: start.ContentBlock.Text}, nil
				}
			case "tool_use":
				s.tool = &domain.ToolCall{
					ID:        start.ContentBlock.ID,
					Name:      start.ContentBlock.Name,
					Arguments: append([]byte(nil), start.ContentBlock.Input...),
				}
			}

		case "content_block_stop":
			if s.tool != nil {
				call := *s.tool
				s.tool = nil
				return provider.StreamEvent{Kind: provider.EventToolCall, ToolCall: &call}, nil
			}

		case "message_delta":
			var delta struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(ev.Data, &delta); err != nil {
				return provider.StreamEvent{}, fmt.Errorf("anthropic: decode message_delta: %w", err)
			}
			if delta.Delta.StopReason != "" {
				s.usage.CompletionTokens = delta.Usage.OutputTokens
				s.usage.TotalTokens = s.usage.PromptTokens + s.usage.CompletionTokens
				usage := s.usage
				return provider.StreamEvent{
					Kind:       provider.EventDone,
					StopReason: mapStopReason(delta.Delta.StopReason),
					Usage:      &usage,
				}, nil
			}

		case "message_stop":
			return provider.StreamEvent{}, io.EOF
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
	case "tool_use":
		return domain.StopToolUse
	case "max_tokens":
		return domain.StopMaxTokens
	case "stop_sequence":
		return domain.StopStop
	case "end_turn":
		return domain.StopEndTurn
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

var _ provider.Provider = (*Client)(nil)
