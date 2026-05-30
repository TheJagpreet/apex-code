// Package provider defines the single interface every model backend implements
// and the streaming abstraction completions are delivered through.
//
// The rest of apex-code depends only on these types, never on a concrete
// vendor SDK. Adapters live in subpackages (e.g. provider/ollama).
package provider

import (
	"context"

	"github.com/apex-code/apex/internal/domain"
)

// Caps describes what a provider/model can do and how big its context is. The
// Budget Manager (plan Phase 2) sizes its token pools from ContextWindow and
// reserves MaxOutputTokens of headroom.
type Caps struct {
	// ContextWindow is the total token budget (input + output) the model
	// accepts. Adapters that can introspect the model (e.g. Ollama /api/show,
	// plan step 1.7) populate this dynamically; otherwise a sane default.
	ContextWindow int

	// MaxOutputTokens is the largest completion the model will produce, if
	// known. Zero means "unknown / provider default".
	MaxOutputTokens int

	SupportsTools       bool
	SupportsStreaming   bool
	SupportsPromptCache bool
}

// Provider is the one interface every model backend implements.
//
// Complete returns a Stream the caller drains; the caller owns calling
// Close on it. CountTokens gives the Budget Manager an exact (or best-effort)
// pre-flight measurement so a Request is never sent over budget. Capabilities
// is cheap and may be cached by the caller.
type Provider interface {
	// Name is a short, stable identifier for the backend (e.g. "ollama").
	Name() string

	// Complete starts a streaming completion. The returned Stream yields
	// events until io.EOF. ctx cancellation aborts the in-flight request.
	Complete(ctx context.Context, req domain.Request) (Stream, error)

	// CountTokens measures how many tokens the given messages occupy for this
	// provider's active model. Implementations should be exact where a real
	// tokenizer is available (plan step 1.8) and clearly heuristic otherwise.
	CountTokens(ctx context.Context, messages []domain.Message) (int, error)

	// Capabilities reports the active model's limits and features.
	Capabilities() Caps
}
