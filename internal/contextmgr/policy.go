package contextmgr

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
)

func (m *Manager) elideDuplicates(ctx context.Context, items []Item, report *RenderReport) ([]Item, error) {
	seen := map[string]string{}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		key := item.Meta.Hash
		if key == "" {
			key = hashMessage(item.Message)
		}
		if firstID, ok := seen[key]; ok && !item.Meta.Pinned {
			before, err := m.count(ctx, item.Message)
			if err != nil {
				return nil, err
			}
			item.Message = domain.Message{
				Role:    item.Message.Role,
				Content: fmt.Sprintf("unchanged duplicate of %s", firstID),
			}
			item.Meta.Kind = ItemSummary
			item.Meta.Source = SourceSummary
			item.Meta.Pool = agent.PoolHistory
			after, err := m.count(ctx, item.Message)
			if err != nil {
				return nil, err
			}
			report.SavedBy["elision"] += max(0, before-after)
			report.Elided = append(report.Elided, item.Meta.ID)
		} else {
			seen[key] = item.Meta.ID
		}
		out = append(out, item)
	}
	return out, nil
}

func (m *Manager) digestStaleToolResults(ctx context.Context, items []Item, report *RenderReport) ([]Item, error) {
	for i := range items {
		if items[i].Meta.Kind != ItemToolResult || items[i].Meta.Pinned || !items[i].Meta.Stale {
			continue
		}
		before, err := m.count(ctx, items[i].Message)
		if err != nil {
			return nil, err
		}
		digest := items[i].Digest
		if digest == "" {
			digest = m.digestToolResult(items[i])
		}
		items[i].Message = domain.Message{Role: domain.RoleTool, ToolResults: []domain.ToolResult{{
			ToolCallID: firstToolID(items[i].Message),
			Content:    digest,
		}}}
		items[i].Meta.Kind = ItemSummary
		items[i].Meta.Source = SourceSummary
		after, err := m.count(ctx, items[i].Message)
		if err != nil {
			return nil, err
		}
		report.SavedBy["digest"] += max(0, before-after)
		report.Digested = append(report.Digested, items[i].Meta.ID)
	}
	return items, nil
}

func (m *Manager) digestToolResult(item Item) string {
	text := compactWhitespace(messageText(item.Message))
	if text == "" {
		text = "empty tool result"
	}
	if len(text) > m.opts.MaxDigestChars {
		text = text[:m.opts.MaxDigestChars] + "..."
	}
	return "digest: " + text
}

func (m *Manager) evictToPoolTargets(ctx context.Context, items []Item, budget agent.Budget, report *RenderReport) ([]Item, error) {
	return m.evict(ctx, items, func(candidate []Item) (bool, error) {
		_, byPool, err := m.measurePools(ctx, candidate)
		if err != nil {
			return false, err
		}
		for pool, used := range byPool {
			if pool == agent.PoolOutputHeadroom {
				continue
			}
			if limit := budget.Pools[pool]; limit > 0 && used > limit {
				return false, nil
			}
		}
		return true, nil
	}, report, "eviction")
}

func (m *Manager) evictToPromptLimit(ctx context.Context, items []Item, budget agent.Budget, report *RenderReport) ([]Item, error) {
	return m.evict(ctx, items, func(candidate []Item) (bool, error) {
		total, _, err := m.measurePools(ctx, candidate)
		if err != nil {
			return false, err
		}
		return total <= budget.PromptLimit, nil
	}, report, "eviction")
}

func (m *Manager) evict(ctx context.Context, items []Item, fits func([]Item) (bool, error), report *RenderReport, label string) ([]Item, error) {
	ok, err := fits(items)
	if err != nil || ok {
		return items, err
	}

	for {
		idx := evictionCandidate(items)
		if idx < 0 {
			return items, fmt.Errorf("context manager: pinned items exceed budget")
		}
		before, err := m.count(ctx, items[idx].Message)
		if err != nil {
			return nil, err
		}
		report.SavedBy[label] += before
		report.Evicted = append(report.Evicted, items[idx].Meta.ID)
		items = append(items[:idx], items[idx+1:]...)

		ok, err := fits(items)
		if err != nil || ok {
			return items, err
		}
	}
}

func (m *Manager) summarizeOldHistory(ctx context.Context, items []Item, budget agent.Budget, report *RenderReport) ([]Item, error) {
	total, byPool, err := m.measurePools(ctx, items)
	if err != nil {
		return nil, err
	}
	if total <= budget.PromptLimit && byPool[agent.PoolHistory] <= budget.Pools[agent.PoolHistory] {
		return items, nil
	}

	old := historyCandidates(items)
	if len(old) < 2 {
		return items, nil
	}
	block := make([]domain.Message, 0, len(old))
	for _, idx := range old {
		block = append(block, items[idx].Message)
	}

	summary, err := m.summary(ctx, block)
	if err != nil {
		return nil, err
	}
	before, err := m.provider.CountTokens(ctx, block)
	if err != nil {
		return nil, err
	}
	summaryItem := Item{
		Meta: Metadata{
			ID:       "summary:" + hashMessages(block)[:12],
			Kind:     ItemSummary,
			Pool:     agent.PoolHistory,
			Source:   SourceSummary,
			LastUsed: items[old[len(old)-1]].Meta.LastUsed,
			Hash:     hashText(summary),
		},
		Message: domain.Message{Role: domain.RoleUser, Content: "story so far: " + summary},
	}
	after, err := m.count(ctx, summaryItem.Message)
	if err != nil {
		return nil, err
	}

	remove := map[int]bool{}
	for _, idx := range old {
		remove[idx] = true
		report.Summarized = append(report.Summarized, items[idx].Meta.ID)
	}
	next := make([]Item, 0, len(items)-len(old)+1)
	inserted := false
	for i, item := range items {
		if remove[i] {
			if !inserted {
				next = append(next, summaryItem)
				inserted = true
			}
			continue
		}
		next = append(next, item)
	}
	report.SavedBy["summary"] += max(0, before-after)
	return next, nil
}

func (m *Manager) summary(ctx context.Context, messages []domain.Message) (string, error) {
	key := hashMessages(messages)
	if cached, ok := m.cache[key]; ok {
		return cached, nil
	}
	req := domain.Request{
		Messages: []domain.Message{
			{Role: domain.RoleSystem, Content: "Summarize the conversation compactly for future coding context. Plain text, one short paragraph."},
			{Role: domain.RoleUser, Content: joinMessageTexts(messages)},
		},
		MaxTokens: 160,
	}
	stream, err := m.provider.Complete(ctx, req)
	if err != nil {
		fallback := heuristicSummary(messages, m.opts.MaxDigestChars)
		m.cache[key] = fallback
		return fallback, nil
	}
	defer stream.Close()

	var parts []string
	for {
		ev, recvErr := stream.Recv()
		if recvErr != nil {
			break
		}
		if ev.Text != "" {
			parts = append(parts, ev.Text)
		}
	}
	summary := compactWhitespace(strings.Join(parts, ""))
	if summary == "" {
		summary = heuristicSummary(messages, m.opts.MaxDigestChars)
	}
	m.cache[key] = summary
	return summary, nil
}

func evictionCandidate(items []Item) int {
	type candidate struct {
		index int
		score int
	}
	candidates := make([]candidate, 0, len(items))
	for i, item := range items {
		if item.Meta.Pinned || item.Meta.Kind == ItemSystem {
			continue
		}
		score := 0
		if item.Meta.Kind == ItemSummary {
			score += 20
		}
		if item.Meta.Kind == ItemToolResult {
			score += 10
		}
		score -= i
		candidates = append(candidates, candidate{index: i, score: score})
	}
	if len(candidates) == 0 {
		return -1
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return items[candidates[i].index].Meta.LastUsed.Before(items[candidates[j].index].Meta.LastUsed)
		}
		return candidates[i].score > candidates[j].score
	})
	return candidates[0].index
}

func historyCandidates(items []Item) []int {
	out := make([]int, 0)
	for i, item := range items {
		if item.Meta.Pinned || item.Meta.Kind == ItemSystem || item.Meta.Kind == ItemSummary {
			continue
		}
		if item.Meta.Pool == agent.PoolHistory {
			out = append(out, i)
		}
	}
	if len(out) > 4 {
		out = out[:len(out)-2]
	}
	return out
}

func firstToolID(msg domain.Message) string {
	if len(msg.ToolResults) > 0 {
		return msg.ToolResults[0].ToolCallID
	}
	return "tool"
}
