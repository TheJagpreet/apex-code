// Package domain defines the core, provider-agnostic types used throughout
// apex-code: messages, tool calls/results, requests, responses, and usage.
//
// These types are deliberately minimal and serializable. Every provider
// adapter (Ollama, Anthropic, OpenAI-compatible) maps to and from them so the
// rest of the system never depends on a vendor-specific shape.
package domain

import "encoding/json"

// Role identifies who produced a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool carries the result of a tool invocation back to the model.
	RoleTool Role = "tool"
)

// StopReason explains why the model stopped generating. Adapters normalize
// their provider-specific reasons into one of these values.
type StopReason string

const (
	StopUnknown   StopReason = ""
	StopEndTurn   StopReason = "end_turn"   // natural completion
	StopToolUse   StopReason = "tool_use"   // model wants to call tool(s)
	StopMaxTokens StopReason = "max_tokens" // hit the output length limit
	StopStop      StopReason = "stop"       // hit a stop sequence
)

// ToolCall is a model-requested invocation of a registered tool. Arguments is
// the raw JSON object the model produced; it is validated against the tool's
// schema by the registry, not here.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult is the outcome of executing a ToolCall, fed back to the model on
// the next turn. Content is already summarized/capped by the time it reaches a
// provider — adapters do not truncate.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Message is a single entry in a conversation.
//
//   - For RoleSystem/RoleUser: Content holds the text.
//   - For RoleAssistant: Content holds text and/or ToolCalls is populated.
//   - For RoleTool: ToolResults carries one or more results.
type Message struct {
	Role         Role         `json:"role"`
	Content      string       `json:"content,omitempty"`
	ToolCalls    []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults  []ToolResult `json:"tool_results,omitempty"`
	CacheControl string       `json:"cache_control,omitempty"`
}

// ToolSpec describes a tool offered to the model. Parameters is a JSON Schema
// object. The terse name+description pair is what gets listed cheaply in
// prompts; the full Parameters schema is injected lazily (see plan Phase 8).
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Usage reports token accounting for a single Complete call. Adapters fill
// whatever the provider reports; zero means "not reported".
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_tokens,omitempty"`
}

// Request is a provider-agnostic completion request. The Budget Manager
// (plan Phase 2) guarantees the assembled Messages fit the model's window
// before a Request is ever handed to a Provider.
type Request struct {
	Model       string     `json:"model"`
	Messages    []Message  `json:"messages"`
	Tools       []ToolSpec `json:"tools,omitempty"`
	Temperature float64    `json:"temperature,omitempty"`
	// MaxTokens caps generated output; 0 means "use the provider default".
	MaxTokens int      `json:"max_tokens,omitempty"`
	Stop      []string `json:"stop,omitempty"`
	KeepAlive string   `json:"keep_alive,omitempty"`
}

// Response is the fully-collected result of a Complete call once its stream has
// been drained. The agent loop typically consumes the Stream directly, but
// Response is convenient for tests and non-streaming call sites.
type Response struct {
	Model      string     `json:"model"`
	Message    Message    `json:"message"`
	StopReason StopReason `json:"stop_reason"`
	Usage      Usage      `json:"usage"`
}
