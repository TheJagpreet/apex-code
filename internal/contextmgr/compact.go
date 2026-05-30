package contextmgr

import (
	"context"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/domain"
)

type Compactor struct {
	manager *Manager
}

func (c *Compactor) Compact(ctx context.Context, messages []domain.Message, _ agent.BudgetReport, budget agent.Budget) ([]domain.Message, error) {
	ws := c.manager.FromMessages(messages)
	prompt, err := c.manager.Render(ctx, ws, budget)
	if err != nil {
		return nil, err
	}
	return prompt.Messages, nil
}

var _ agent.Compactor = (*Compactor)(nil)
