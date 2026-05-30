package agent

import (
	"context"

	"github.com/apex-code/apex/internal/domain"
)

type NopCompactor struct{}

func (NopCompactor) Compact(_ context.Context, messages []domain.Message, _ BudgetReport, _ Budget) ([]domain.Message, error) {
	return cloneMessages(messages), nil
}
