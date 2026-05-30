package contextmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
)

type Manager struct {
	provider provider.Provider
	opts     Options
	cache    map[string]string
	now      func() time.Time
}

func New(p provider.Provider, opts Options) *Manager {
	def := DefaultOptions()
	if opts.MaxDigestChars <= 0 {
		opts.MaxDigestChars = def.MaxDigestChars
	}
	return &Manager{
		provider: p,
		opts:     opts,
		cache:    map[string]string{},
		now:      time.Now,
	}
}

func (m *Manager) FromMessages(messages []domain.Message) WorkingSet {
	items := make([]Item, 0, len(messages))
	base := m.now().Add(-time.Duration(len(messages)) * time.Minute)
	for i, msg := range messages {
		meta := Metadata{
			ID:       fmt.Sprintf("message:%04d:%s", i, hashMessage(msg)[:12]),
			Kind:     ItemHistory,
			Pool:     agent.PoolHistory,
			Source:   SourceMessage,
			LastUsed: base.Add(time.Duration(i) * time.Minute),
			Hash:     hashMessage(msg),
		}
		switch msg.Role {
		case domain.RoleSystem:
			meta.Kind = ItemSystem
			meta.Pool = agent.PoolSystem
			meta.Pinned = true
		case domain.RoleTool:
			meta.Kind = ItemToolResult
			meta.Pool = agent.PoolHistory
			meta.Source = SourceTool
			meta.Stale = i < len(messages)-2
			meta.ID = fmt.Sprintf("tool:%04d:%s", i, hashMessage(msg)[:12])
		default:
			if i == len(messages)-1 {
				meta.Pinned = true
			}
		}
		items = append(items, Item{Meta: meta, Message: cloneMessage(msg)})
	}
	return WorkingSet{Items: items}
}

func (m *Manager) AddRetrievedContext(ws WorkingSet, id, content string, pinned bool) WorkingSet {
	if strings.TrimSpace(content) == "" {
		return ws
	}
	item := Item{
		Meta: Metadata{
			ID:       "retrieved:" + id,
			Kind:     ItemRetrieved,
			Pool:     agent.PoolRetrieved,
			Source:   SourceFile,
			Path:     id,
			LastUsed: m.now(),
			Pinned:   pinned,
			Hash:     hashText(content),
		},
		Message: domain.Message{Role: domain.RoleSystem, Content: "retrieved context:\n" + content},
	}
	next := WorkingSet{Items: cloneItems(ws.Items)}
	next.Items = append(next.Items, item)
	return next
}

func (m *Manager) Render(ctx context.Context, ws WorkingSet, budget agent.Budget) (Prompt, error) {
	items := cloneItems(ws.Items)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Meta.LastUsed.Before(items[j].Meta.LastUsed)
	})

	inTokens, err := m.measureItems(ctx, items)
	if err != nil {
		return Prompt{}, err
	}

	report := RenderReport{
		ContextWindow: budget.TotalWindow,
		PromptLimit:   budget.PromptLimit,
		TokensByPool:  map[agent.PoolName]int{},
		LimitsByPool:  cloneLimits(budget.Pools),
		TokensIn:      inTokens,
		SavedBy:       map[string]int{},
	}

	items, err = m.elideDuplicates(ctx, items, &report)
	if err != nil {
		return Prompt{}, err
	}
	items, err = m.digestStaleToolResults(ctx, items, &report)
	if err != nil {
		return Prompt{}, err
	}
	items, err = m.summarizeOldHistory(ctx, items, budget, &report)
	if err != nil {
		return Prompt{}, err
	}
	items, err = m.evictToPoolTargets(ctx, items, budget, &report)
	if err != nil {
		return Prompt{}, err
	}
	items, err = m.evictToPromptLimit(ctx, items, budget, &report)
	if err != nil {
		return Prompt{}, err
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Meta.LastUsed.Before(items[j].Meta.LastUsed)
	})
	messages := make([]domain.Message, 0, len(items))
	for _, item := range items {
		messages = append(messages, cloneMessage(item.Message))
	}

	outTokens, byPool, err := m.measurePools(ctx, items)
	if err != nil {
		return Prompt{}, err
	}
	report.TokensOut = outTokens
	report.TokensSaved = max(0, report.TokensIn-report.TokensOut)
	report.TokensByPool = byPool
	if m.opts.Logger != nil {
		m.opts.Logger.Render(report)
	}
	return Prompt{Messages: messages, Report: report}, nil
}

func (m *Manager) Compactor() *Compactor {
	return &Compactor{manager: m}
}

// Messages renders a working set back into a deterministic message slice. This
// is used by session resume so we restore the compact curated window rather
// than replaying a raw transcript.
func (m *Manager) Messages(ws WorkingSet) []domain.Message {
	items := cloneItems(ws.Items)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Meta.LastUsed.Before(items[j].Meta.LastUsed)
	})
	out := make([]domain.Message, 0, len(items))
	for _, item := range items {
		out = append(out, cloneMessage(item.Message))
	}
	return out
}

// Rehydrate validates persisted source-backed items and drops anything stale so
// resumed sessions do not keep outdated file-derived context around.
func (m *Manager) Rehydrate(ws WorkingSet) WorkingSet {
	next := WorkingSet{Items: make([]Item, 0, len(ws.Items))}
	for _, item := range cloneItems(ws.Items) {
		if staleSourceItem(item) {
			if ok := m.itemHashMatches(item); !ok {
				continue
			}
		}
		next.Items = append(next.Items, item)
	}
	return next
}

func (m *Manager) measureItems(ctx context.Context, items []Item) (int, error) {
	total := 0
	for i := range items {
		n, err := m.count(ctx, items[i].Message)
		if err != nil {
			return 0, err
		}
		items[i].Meta.TokenSize = n
		total += n
	}
	return total, nil
}

func (m *Manager) measurePools(ctx context.Context, items []Item) (int, map[agent.PoolName]int, error) {
	total := 0
	byPool := map[agent.PoolName]int{}
	for _, item := range items {
		n, err := m.count(ctx, item.Message)
		if err != nil {
			return 0, nil, err
		}
		total += n
		byPool[item.Meta.Pool] += n
	}
	return total, byPool, nil
}

func (m *Manager) count(ctx context.Context, msg domain.Message) (int, error) {
	return m.provider.CountTokens(ctx, []domain.Message{msg})
}

func hashMessage(msg domain.Message) string {
	h := sha256.New()
	h.Write([]byte(msg.Role))
	h.Write([]byte{0})
	h.Write([]byte(msg.Content))
	for _, call := range msg.ToolCalls {
		h.Write([]byte(call.ID))
		h.Write([]byte(call.Name))
		h.Write(call.Arguments)
	}
	for _, result := range msg.ToolResults {
		h.Write([]byte(result.ToolCallID))
		h.Write([]byte(result.Content))
		if result.IsError {
			h.Write([]byte{1})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func cloneItems(items []Item) []Item {
	out := make([]Item, 0, len(items))
	for _, item := range items {
		c := item
		c.Message = cloneMessage(item.Message)
		out = append(out, c)
	}
	return out
}

func cloneMessage(msg domain.Message) domain.Message {
	out := domain.Message{Role: msg.Role, Content: msg.Content, CacheControl: msg.CacheControl}
	out.ToolCalls = append([]domain.ToolCall(nil), msg.ToolCalls...)
	out.ToolResults = append([]domain.ToolResult(nil), msg.ToolResults...)
	return out
}

func cloneLimits(in map[agent.PoolName]int) map[agent.PoolName]int {
	out := make(map[agent.PoolName]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func staleSourceItem(item Item) bool {
	if item.Meta.Path == "" {
		return false
	}
	switch item.Meta.Source {
	case SourceFile:
		return true
	default:
		return item.Meta.Kind == ItemPinnedFile || item.Meta.Kind == ItemRetrieved
	}
}

func (m *Manager) itemHashMatches(item Item) bool {
	if item.Meta.Hash == "" || item.Meta.Path == "" {
		return true
	}
	data, err := os.ReadFile(item.Meta.Path)
	if err != nil {
		return false
	}
	return hashText(string(data)) == item.Meta.Hash
}

func messageText(msg domain.Message) string {
	var b strings.Builder
	b.WriteString(msg.Content)
	for _, call := range msg.ToolCalls {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("tool_call ")
		b.WriteString(call.Name)
		b.WriteByte(' ')
		b.Write(call.Arguments)
	}
	for _, result := range msg.ToolResults {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("tool_result ")
		b.WriteString(result.ToolCallID)
		b.WriteString(": ")
		b.WriteString(result.Content)
	}
	return b.String()
}
