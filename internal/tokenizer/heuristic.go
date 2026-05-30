package tokenizer

const defaultCharsPerToken = 4

// HeuristicTokenizer is the low-precision fallback for unknown models.
type HeuristicTokenizer struct {
	charsPerToken int
}

func NewHeuristic() HeuristicTokenizer {
	return HeuristicTokenizer{charsPerToken: defaultCharsPerToken}
}

func (h HeuristicTokenizer) Name() string { return "heuristic-char-count" }

func (h HeuristicTokenizer) Exact() bool { return false }

func (h HeuristicTokenizer) Count(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	n := len(text)
	return (n + h.charsPerToken - 1) / h.charsPerToken, nil
}
