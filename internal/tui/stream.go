package tui

import (
	"strings"
)

// scrambleGlyphs is the pool the shimmer draws from: letters, digits, and a few
// blocky symbols give the moving patch a "static being woven into words" feel.
const scrambleGlyphs = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@#%&*+=/\\<>?░▒▓"

// streamFx animates streamed assistant text. Characters arrive at the tail as a
// shimmering patch of random glyphs and then settle left-to-right into the real
// text, as if the model is weaving noise into words. It is the text analogue of
// the footer pet: pure state advanced one frame per animation tick.
type streamFx struct {
	runes []rune // real text received so far
	front int    // runes[:front] are settled; runes[front:] still shimmer
	frame int
}

// active reports whether there is anything to draw.
func (s streamFx) active() bool { return len(s.runes) > 0 }

// append adds a freshly streamed delta to the shimmering tail.
func (s *streamFx) append(text string) {
	s.runes = append(s.runes, []rune(text)...)
}

// advance settles part of the shimmering tail and bumps the frame so unsettled
// glyphs reshuffle. The settle rate scales with the backlog so a fast token
// stream never grows an unbounded patch of noise.
func (s *streamFx) advance() {
	s.frame++
	pending := len(s.runes) - s.front
	if pending <= 0 {
		return
	}
	step := 1 + pending/8
	for i := 0; i < step && s.front < len(s.runes); i++ {
		s.front++
	}
}

// render draws the settled prefix as plain text and the trailing window as a
// shimmering patch of random glyphs in an accent color.
func (s streamFx) render() string {
	if len(s.runes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(styleStreamLive.Render(string(s.runes[:s.front])))
	if s.front >= len(s.runes) {
		return b.String()
	}
	glyphs := []rune(scrambleGlyphs)
	tail := make([]rune, 0, len(s.runes)-s.front)
	for i := s.front; i < len(s.runes); i++ {
		r := s.runes[i]
		// Preserve whitespace so word shapes and line breaks emerge while the
		// patch is still resolving.
		if r == '\n' || r == ' ' || r == '\t' {
			tail = append(tail, r)
			continue
		}
		tail = append(tail, glyphs[shimmerHash(s.frame, i)%len(glyphs)])
	}
	b.WriteString(styleStreamScramble.Render(string(tail)))
	return b.String()
}

// shimmerHash is a cheap deterministic hash so a glyph is stable within a frame
// (render may run several times per frame) but reshuffles as the frame advances.
func shimmerHash(frame, idx int) int {
	x := frame*2654435761 + idx*40503 + 0x9e3779b9
	if x < 0 {
		x = -x
	}
	return x
}

// renderStreamEntry frames the live, in-progress assistant text like a normal
// transcript entry, minus markdown rendering (the text is still partial).
func renderStreamEntry(fx streamFx) string {
	lineWidth := 72
	rule := styleDim.Render(strings.Repeat("─", lineWidth))
	var b strings.Builder
	b.WriteString(rule)
	b.WriteString("\n" + stylePaneTitle.Render("apex") + " " + styleDim.Render("streaming…"))
	b.WriteString("\n" + fx.render())
	b.WriteString("\n" + rule)
	return b.String()
}
