package tools

import (
	"context"
	"encoding/json"
)

// Tool is the common interface deterministic Go tools implement.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Invoke(ctx context.Context, args json.RawMessage) (Result, error)
	EstimateTokenCost(args json.RawMessage) int
}

// Result is the compact, model-facing output from a tool invocation.
// Payload is always plain text and already passed through the summarization
// gate before it reaches the agent loop.
type Result struct {
	ToolName  string
	Payload   string
	Summary   string
	Truncated bool
	TokenCost int
	IsError   bool
	Metadata  map[string]string
}

type GateOptions struct {
	MaxChars      int
	MaxLines      int
	TailChars     int
	SummaryMaxLen int
}

func DefaultGateOptions() GateOptions {
	return GateOptions{
		MaxChars:      2400,
		MaxLines:      80,
		TailChars:     400,
		SummaryMaxLen: 160,
	}
}
