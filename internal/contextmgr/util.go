package contextmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/apex-code/apex/internal/domain"
)

func hashMessages(messages []domain.Message) string {
	h := sha256.New()
	for _, msg := range messages {
		h.Write([]byte(hashMessage(msg)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func joinMessageTexts(messages []domain.Message) string {
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		text := messageText(msg)
		if text == "" {
			continue
		}
		parts = append(parts, string(msg.Role)+": "+text)
	}
	return strings.Join(parts, "\n")
}

func heuristicSummary(messages []domain.Message, limit int) string {
	text := compactWhitespace(joinMessageTexts(messages))
	if limit <= 0 {
		limit = 180
	}
	if len(text) > limit {
		text = text[:limit] + "..."
	}
	if text == "" {
		return "no prior details"
	}
	return text
}

func compactWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
