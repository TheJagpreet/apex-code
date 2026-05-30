package tokenizer_test

import (
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/tokenizer"
)

func TestHeuristicTokenizer(t *testing.T) {
	tok := tokenizer.NewHeuristic()
	n, err := tok.Count("12345678")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Fatalf("Count = %d", n)
	}
}

func TestSentencePieceTokenizerCount(t *testing.T) {
	tok, err := tokenizer.NewSentencePiece("gemma-test", []string{
		"<unk>", "<s>", "</s>", "▁hello", "▁world", "!", "he", "llo", "wor", "ld", "<0x0A>",
	}, []float64{
		0, 0, 0, 10, 10, 5, 1, 1, 1, 1, 0,
	})
	if err != nil {
		t.Fatalf("NewSentencePiece: %v", err)
	}
	n, err := tok.Count("hello world!")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Fatalf("Count = %d", n)
	}
}

func TestCountMessages(t *testing.T) {
	tok := tokenizer.NewHeuristic()
	n, err := tokenizer.CountMessages(tok, []domain.Message{
		{Role: domain.RoleUser, Content: "12345678"},
	})
	if err != nil {
		t.Fatalf("CountMessages: %v", err)
	}
	if n != 3 {
		t.Fatalf("CountMessages = %d", n)
	}
}
