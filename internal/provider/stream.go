package provider

import "github.com/apex-code/apex/internal/domain"

// EventKind discriminates the payload carried by a StreamEvent.
type EventKind int

const (
	// EventText is an incremental chunk of assistant text. Text holds the
	// delta (not the cumulative string).
	EventText EventKind = iota

	// EventToolCall signals the model is requesting a tool. ToolCall is set.
	// A provider may emit a tool call incrementally; adapters in apex-code
	// coalesce partial calls and emit one complete EventToolCall.
	EventToolCall

	// EventUsage reports token accounting. Usage is set. Typically emitted
	// once, near the end of a stream.
	EventUsage

	// EventDone is the terminal event before io.EOF; StopReason explains why
	// generation stopped.
	EventDone
)

// StreamEvent is one item produced by a Stream. Exactly one payload field is
// meaningful per Kind.
type StreamEvent struct {
	Kind       EventKind
	Text       string
	ToolCall   *domain.ToolCall
	Usage      *domain.Usage
	StopReason domain.StopReason
}

// Stream delivers completion events incrementally.
//
// Recv blocks until the next event is available and returns io.EOF once the
// stream is exhausted (after the EventDone). Any other returned error is
// terminal. Callers must call Close to release resources, even on error.
type Stream interface {
	// Recv returns the next event, or io.EOF when finished.
	Recv() (StreamEvent, error)
	// Close releases underlying resources (e.g. the HTTP response body). It is
	// safe to call more than once.
	Close() error
}
