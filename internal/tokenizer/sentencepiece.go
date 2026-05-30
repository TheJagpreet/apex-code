package tokenizer

import (
	"fmt"
	"strings"
	"unicode"
)

const sentencePieceSpace = "▁"

type spPiece struct {
	text  string
	score float64
}

// SentencePieceTokenizer runs a small unigram Viterbi pass over a model's
// exported SentencePiece-style vocabulary.
type SentencePieceTokenizer struct {
	name         string
	piecesByByte map[byte][]spPiece
	byteFallback map[byte]spPiece
}

func NewSentencePiece(name string, pieces []string, scores []float64) (*SentencePieceTokenizer, error) {
	if len(pieces) == 0 {
		return nil, fmt.Errorf("sentencepiece: empty vocabulary")
	}

	tok := &SentencePieceTokenizer{
		name:         name,
		piecesByByte: make(map[byte][]spPiece),
		byteFallback: make(map[byte]spPiece),
	}

	for i, piece := range pieces {
		if piece == "" {
			continue
		}

		score := 0.0
		if i < len(scores) {
			score = scores[i]
		}

		if b, ok := parseByteFallback(piece); ok {
			tok.byteFallback[b] = spPiece{text: piece, score: score}
			continue
		}

		if isSpecialPiece(piece) {
			continue
		}

		first := piece[0]
		tok.piecesByByte[first] = append(tok.piecesByByte[first], spPiece{text: piece, score: score})
	}

	if len(tok.piecesByByte) == 0 && len(tok.byteFallback) == 0 {
		return nil, fmt.Errorf("sentencepiece: no usable vocabulary pieces")
	}

	return tok, nil
}

func (t *SentencePieceTokenizer) Name() string { return t.name }

func (t *SentencePieceTokenizer) Exact() bool { return true }

func (t *SentencePieceTokenizer) Count(text string) (int, error) {
	normalized := normalizeSentencePieceInput(text)
	if normalized == "" {
		return 0, nil
	}

	bestScore := make([]float64, len(normalized)+1)
	bestCount := make([]int, len(normalized)+1)
	reachable := make([]bool, len(normalized)+1)
	reachable[0] = true

	for i := 0; i < len(normalized); i++ {
		if !reachable[i] {
			continue
		}

		for _, piece := range t.piecesByByte[normalized[i]] {
			if !strings.HasPrefix(normalized[i:], piece.text) {
				continue
			}
			j := i + len(piece.text)
			nextScore := bestScore[i] + piece.score
			nextCount := bestCount[i] + 1
			if !reachable[j] || nextScore > bestScore[j] || (nextScore == bestScore[j] && nextCount < bestCount[j]) {
				reachable[j] = true
				bestScore[j] = nextScore
				bestCount[j] = nextCount
			}
		}

		if piece, ok := t.byteFallback[normalized[i]]; ok {
			j := i + 1
			nextScore := bestScore[i] + piece.score
			nextCount := bestCount[i] + 1
			if !reachable[j] || nextScore > bestScore[j] || (nextScore == bestScore[j] && nextCount < bestCount[j]) {
				reachable[j] = true
				bestScore[j] = nextScore
				bestCount[j] = nextCount
			}
		}
	}

	if !reachable[len(normalized)] {
		return 0, fmt.Errorf("sentencepiece: no tokenization path for input")
	}

	return bestCount[len(normalized)], nil
}

func normalizeSentencePieceInput(text string) string {
	var b strings.Builder
	atWordStart := true
	for _, r := range text {
		if unicode.IsSpace(r) {
			atWordStart = true
			continue
		}
		if atWordStart {
			b.WriteString(sentencePieceSpace)
			atWordStart = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func parseByteFallback(piece string) (byte, bool) {
	if len(piece) != 6 || !strings.HasPrefix(piece, "<0x") || piece[5] != '>' {
		return 0, false
	}

	var b byte
	for i := 3; i < 5; i++ {
		b <<= 4
		switch c := piece[i]; {
		case c >= '0' && c <= '9':
			b |= c - '0'
		case c >= 'A' && c <= 'F':
			b |= c - 'A' + 10
		default:
			return 0, false
		}
	}
	return b, true
}

func isSpecialPiece(piece string) bool {
	return strings.HasPrefix(piece, "<") && strings.HasSuffix(piece, ">")
}
