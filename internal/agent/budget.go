package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider"
)

type PoolName string

const (
	PoolSystem         PoolName = "system"
	PoolTools          PoolName = "tools"
	PoolHistory        PoolName = "history"
	PoolRetrieved      PoolName = "retrieved-context"
	PoolWorkingFiles   PoolName = "working-files"
	PoolOutputHeadroom PoolName = "output-headroom"
)

var ErrBudgetExceeded = errors.New("agent: prompt exceeds budget")

type BudgetFractions struct {
	System         float64
	Tools          float64
	History        float64
	Retrieved      float64
	WorkingFiles   float64
	OutputHeadroom float64
}

func DefaultBudgetFractions() BudgetFractions {
	return BudgetFractions{
		System:         0.10,
		Tools:          0.10,
		History:        0.40,
		Retrieved:      0.15,
		WorkingFiles:   0.15,
		OutputHeadroom: 0.10,
	}
}

type Budget struct {
	TotalWindow    int
	PromptLimit    int
	OutputHeadroom int
	Pools          map[PoolName]int
	Fractions      BudgetFractions
}

type BudgetReport struct {
	ContextWindow     int
	PromptLimit       int
	OutputHeadroom    int
	TokensByPool      map[PoolName]int
	PoolLimits        map[PoolName]int
	OverflowByPool    map[PoolName]int
	TotalPromptTokens int
	WithinBudget      bool
}

type Compactor interface {
	Compact(ctx context.Context, messages []domain.Message, report BudgetReport, budget Budget) ([]domain.Message, error)
}

func buildBudget(caps provider.Caps, opts Options) Budget {
	window := caps.ContextWindow
	if window <= 0 {
		window = 8192
	}

	if !opts.BudgetSet {
		return buildUnboundedBudget(window, caps, opts)
	}

	fractions := opts.BudgetFractions
	if fractions == (BudgetFractions{}) {
		fractions = DefaultBudgetFractions()
	}

	headroom := sizePool(window, fractions.OutputHeadroom)
	if caps.MaxOutputTokens > headroom {
		headroom = caps.MaxOutputTokens
	}
	if opts.MaxTokens > headroom {
		headroom = opts.MaxTokens
	}
	if headroom <= 0 {
		headroom = 1024
	}
	if headroom >= window {
		headroom = window / 4
		if headroom <= 0 {
			headroom = 1
		}
	}

	pools := map[PoolName]int{
		PoolSystem:         sizePool(window, fractions.System),
		PoolTools:          sizePool(window, fractions.Tools),
		PoolHistory:        sizePool(window, fractions.History),
		PoolRetrieved:      sizePool(window, fractions.Retrieved),
		PoolWorkingFiles:   sizePool(window, fractions.WorkingFiles),
		PoolOutputHeadroom: headroom,
	}

	promptLimit := window - headroom
	if promptLimit < 1 {
		promptLimit = 1
	}

	return Budget{
		TotalWindow:    window,
		PromptLimit:    promptLimit,
		OutputHeadroom: headroom,
		Pools:          pools,
		Fractions:      fractions,
	}
}

func buildUnboundedBudget(window int, caps provider.Caps, opts Options) Budget {
	headroom := opts.MaxTokens
	if headroom <= 0 {
		headroom = 1024
	}
	if caps.MaxOutputTokens > 0 && caps.MaxOutputTokens < headroom {
		headroom = caps.MaxOutputTokens
	}
	if headroom >= window {
		headroom = window / 8
		if headroom <= 0 {
			headroom = 1
		}
	}
	promptLimit := window - headroom
	if promptLimit < 1 {
		promptLimit = 1
	}
	return Budget{
		TotalWindow:    window,
		PromptLimit:    promptLimit,
		OutputHeadroom: headroom,
		Pools: map[PoolName]int{
			PoolSystem:         0,
			PoolTools:          0,
			PoolHistory:        0,
			PoolRetrieved:      0,
			PoolWorkingFiles:   0,
			PoolOutputHeadroom: headroom,
		},
		Fractions: opts.BudgetFractions,
	}
}

func measureBudget(ctx context.Context, p provider.Provider, req domain.Request, budget Budget) (BudgetReport, error) {
	report := BudgetReport{
		ContextWindow:  budget.TotalWindow,
		PromptLimit:    budget.PromptLimit,
		OutputHeadroom: budget.OutputHeadroom,
		TokensByPool:   map[PoolName]int{},
		PoolLimits:     clonePoolMap(budget.Pools),
		OverflowByPool: map[PoolName]int{},
	}

	systemMsgs, historyMsgs := splitMessagesForPools(req.Messages)
	systemTokens, err := countMessages(ctx, p, systemMsgs)
	if err != nil {
		return BudgetReport{}, err
	}
	historyTokens, err := countMessages(ctx, p, historyMsgs)
	if err != nil {
		return BudgetReport{}, err
	}
	toolTokens, err := countTools(ctx, p, req.Tools)
	if err != nil {
		return BudgetReport{}, err
	}

	report.TokensByPool[PoolSystem] = systemTokens
	report.TokensByPool[PoolTools] = toolTokens
	report.TokensByPool[PoolHistory] = historyTokens
	report.TokensByPool[PoolRetrieved] = 0
	report.TokensByPool[PoolWorkingFiles] = 0
	report.TokensByPool[PoolOutputHeadroom] = budget.OutputHeadroom

	report.TotalPromptTokens = systemTokens + toolTokens + historyTokens
	report.WithinBudget = report.TotalPromptTokens <= budget.PromptLimit

	for pool, used := range report.TokensByPool {
		limit, ok := report.PoolLimits[pool]
		if !ok || pool == PoolOutputHeadroom {
			continue
		}
		if used > limit {
			report.OverflowByPool[pool] = used - limit
		}
	}

	return report, nil
}

func logBudget(logger *slog.Logger, report BudgetReport) {
	if logger == nil {
		return
	}
	logger.Info("budget report",
		slog.Int("context_window", report.ContextWindow),
		slog.Int("prompt_limit", report.PromptLimit),
		slog.Int("output_headroom", report.OutputHeadroom),
		slog.Int("prompt_tokens", report.TotalPromptTokens),
		slog.Bool("within_budget", report.WithinBudget),
		slog.Any("tokens_by_pool", report.TokensByPool),
		slog.Any("overflow_by_pool", report.OverflowByPool),
	)
}

func sizePool(total int, fraction float64) int {
	if fraction <= 0 {
		return 0
	}
	return int(math.Round(float64(total) * fraction))
}

func countMessages(ctx context.Context, p provider.Provider, messages []domain.Message) (int, error) {
	if len(messages) == 0 {
		return 0, nil
	}
	return p.CountTokens(ctx, messages)
}

func countTools(ctx context.Context, p provider.Provider, tools []domain.ToolSpec) (int, error) {
	if len(tools) == 0 {
		return 0, nil
	}

	body, err := json.Marshal(tools)
	if err != nil {
		return 0, fmt.Errorf("marshal tools for budget count: %w", err)
	}
	return p.CountTokens(ctx, []domain.Message{{
		Role:    domain.RoleSystem,
		Content: string(body),
	}})
}

func splitMessagesForPools(messages []domain.Message) ([]domain.Message, []domain.Message) {
	system := make([]domain.Message, 0)
	history := make([]domain.Message, 0)
	for _, m := range messages {
		if m.Role == domain.RoleSystem {
			system = append(system, m)
			continue
		}
		history = append(history, m)
	}
	return system, history
}

func clonePoolMap(in map[PoolName]int) map[PoolName]int {
	out := make(map[PoolName]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
