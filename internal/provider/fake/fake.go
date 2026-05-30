package fake

import (
	"context"
	"io"
	"sync"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/tokenizer"
)

// Provider is a deterministic in-memory provider used by tests in later
// phases. It can stream a scripted sequence of events and exposes stable
// capabilities without any network dependency.
type Provider struct {
	mu           sync.RWMutex
	name         string
	caps         provider.Caps
	tokenCounter tokenizer.Tokenizer
	events       []provider.StreamEvent
	scripts      [][]provider.StreamEvent
	completeErr  error
	requests     []domain.Request
}

func New(events []provider.StreamEvent) *Provider {
	return &Provider{
		name: "fake",
		caps: provider.Caps{
			ContextWindow:       8192,
			MaxOutputTokens:     1024,
			SupportsTools:       true,
			SupportsStreaming:   true,
			SupportsPromptCache: false,
		},
		tokenCounter: tokenizer.NewHeuristic(),
		events:       append([]provider.StreamEvent(nil), events...),
	}
}

func (p *Provider) WithName(name string) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name = name
	return p
}

func (p *Provider) WithCapabilities(caps provider.Caps) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caps = caps
	return p
}

func (p *Provider) WithCompleteError(err error) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completeErr = err
	return p
}

func (p *Provider) WithScripts(scripts [][]provider.StreamEvent) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scripts = cloneScripts(scripts)
	return p
}

func (p *Provider) Requests() []domain.Request {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]domain.Request, len(p.requests))
	for i, req := range p.requests {
		out[i] = cloneRequest(req)
	}
	return out
}

func (p *Provider) Name() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.name
}

func (p *Provider) Complete(_ context.Context, req domain.Request) (provider.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completeErr != nil {
		return nil, p.completeErr
	}
	p.requests = append(p.requests, req)

	events := append([]provider.StreamEvent(nil), p.events...)
	if len(p.scripts) > 0 {
		events = append([]provider.StreamEvent(nil), p.scripts[0]...)
		p.scripts = p.scripts[1:]
	}
	return &stream{events: events}, nil
}

func (p *Provider) CountTokens(_ context.Context, messages []domain.Message) (int, error) {
	p.mu.RLock()
	tok := p.tokenCounter
	p.mu.RUnlock()
	return tokenizer.CountMessages(tok, messages)
}

func (p *Provider) Capabilities() provider.Caps {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.caps
}

type stream struct {
	events []provider.StreamEvent
	index  int
	closed bool
}

func (s *stream) Recv() (provider.StreamEvent, error) {
	if s.closed || s.index >= len(s.events) {
		return provider.StreamEvent{}, io.EOF
	}
	ev := s.events[s.index]
	s.index++
	return ev, nil
}

func (s *stream) Close() error {
	s.closed = true
	return nil
}

func cloneScripts(in [][]provider.StreamEvent) [][]provider.StreamEvent {
	out := make([][]provider.StreamEvent, 0, len(in))
	for _, script := range in {
		out = append(out, append([]provider.StreamEvent(nil), script...))
	}
	return out
}

var _ provider.Provider = (*Provider)(nil)

func cloneRequest(req domain.Request) domain.Request {
	out := req
	out.Messages = append([]domain.Message(nil), req.Messages...)
	out.Tools = append([]domain.ToolSpec(nil), req.Tools...)
	out.Stop = append([]string(nil), req.Stop...)
	return out
}
