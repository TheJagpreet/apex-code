package tokenizer

import (
	"fmt"

	"github.com/apex-code/apex/internal/domain"
)

// Tokenizer counts tokens for plain text. Providers compose it over their
// message fields until request-shape-specific accounting lands later.
type Tokenizer interface {
	Name() string
	Count(text string) (int, error)
	Exact() bool
}

// CountMessages applies a Tokenizer across apex-code's provider-agnostic
// message shape.
func CountMessages(tok Tokenizer, messages []domain.Message) (int, error) {
	total := 0
	for _, m := range messages {
		n, err := tok.Count(string(m.Role))
		if err != nil {
			return 0, fmt.Errorf("count role: %w", err)
		}
		total += n

		n, err = tok.Count(m.Content)
		if err != nil {
			return 0, fmt.Errorf("count content: %w", err)
		}
		total += n

		for _, tc := range m.ToolCalls {
			n, err = tok.Count(tc.Name)
			if err != nil {
				return 0, fmt.Errorf("count tool-call name: %w", err)
			}
			total += n

			n, err = tok.Count(string(tc.Arguments))
			if err != nil {
				return 0, fmt.Errorf("count tool-call arguments: %w", err)
			}
			total += n
		}

		for _, tr := range m.ToolResults {
			n, err = tok.Count(tr.Content)
			if err != nil {
				return 0, fmt.Errorf("count tool result: %w", err)
			}
			total += n
		}
	}

	return total, nil
}
