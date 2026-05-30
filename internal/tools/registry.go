package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
)

type Registry struct {
	gate  Gate
	tools map[string]Tool
	order []string
}

func NewRegistry(gate Gate) *Registry {
	return &Registry{
		gate:  gate,
		tools: map[string]Tool{},
	}
}

func (r *Registry) Register(tool Tool) error {
	name := strings.TrimSpace(tool.Name())
	if name == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = tool
	r.order = append(r.order, name)
	sort.Strings(r.order)
	return nil
}

func (r *Registry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

func (r *Registry) Describe() []string {
	lines := make([]string, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		lines = append(lines, fmt.Sprintf("%s: %s", tool.Name(), oneLine(tool.Description())))
	}
	return lines
}

func (r *Registry) Specs() []domain.ToolSpec {
	specs := make([]domain.ToolSpec, 0, len(r.order))
	for _, tool := range r.List() {
		specs = append(specs, domain.ToolSpec{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Schema(),
		})
	}
	return specs
}

type Dispatcher struct {
	registry *Registry
}

func NewDispatcher(registry *Registry) *Dispatcher {
	return &Dispatcher{registry: registry}
}

func (d *Dispatcher) DispatchToolCalls(ctx context.Context, calls []domain.ToolCall) ([]domain.ToolResult, error) {
	results := make([]domain.ToolResult, 0, len(calls))
	for _, call := range calls {
		tool, ok := d.registry.Lookup(call.Name)
		if !ok {
			results = append(results, domain.ToolResult{
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("tool not found: %s", call.Name),
				IsError:    true,
			})
			continue
		}

		res, err := tool.Invoke(ctx, call.Arguments)
		if err != nil {
			if res.Payload != "" || res.Summary != "" {
				res.ToolName = tool.Name()
				res.IsError = true
				content := renderToolResult(res) + "\nerror: " + err.Error()
				results = append(results, domain.ToolResult{
					ToolCallID: call.ID,
					Content:    content,
					IsError:    true,
				})
				continue
			}
			results = append(results, domain.ToolResult{
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("tool error: %v", err),
				IsError:    true,
			})
			continue
		}

		res.ToolName = tool.Name()
		results = append(results, domain.ToolResult{
			ToolCallID: call.ID,
			Content:    renderToolResult(res),
			IsError:    res.IsError,
		})
	}
	return results, nil
}

func renderToolResult(res Result) string {
	var b strings.Builder
	b.WriteString("tool: ")
	b.WriteString(res.ToolName)
	b.WriteString("\nsummary: ")
	b.WriteString(res.Summary)
	b.WriteString("\ntokens: ")
	b.WriteString(fmt.Sprintf("%d", res.TokenCost))
	if res.Truncated {
		b.WriteString("\ntruncated: true")
	}
	if res.Payload != "" {
		b.WriteString("\npayload:\n")
		b.WriteString(res.Payload)
	}
	return strings.TrimSpace(b.String())
}

var _ agent.ToolDispatcher = (*Dispatcher)(nil)

func mustJSONSchema(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
