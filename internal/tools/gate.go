package tools

import (
	"fmt"
	"strings"
)

// Gate caps tool output into compact, plain-text, model-friendly payloads.
type Gate struct {
	opts GateOptions
}

func NewGate(opts GateOptions) Gate {
	if opts == (GateOptions{}) {
		opts = DefaultGateOptions()
	}
	return Gate{opts: opts}
}

func (g Gate) Apply(res Result) Result {
	payload := normalizeText(res.Payload)
	summary := oneLine(normalizeText(res.Summary))
	if summary == "" {
		summary = defaultSummary(payload)
	}

	lines := strings.Split(payload, "\n")
	truncated := res.Truncated
	if len(lines) > g.opts.MaxLines {
		lines = append(lines[:g.opts.MaxLines], fmt.Sprintf("... truncated: %d more lines", len(strings.Split(payload, "\n"))-g.opts.MaxLines))
		truncated = true
	}
	payload = strings.Join(lines, "\n")

	if len(payload) > g.opts.MaxChars {
		head := payload[:max(0, g.opts.MaxChars-g.opts.TailChars-32)]
		tail := payload[max(0, len(payload)-g.opts.TailChars):]
		payload = head + "\n... truncated ...\n" + tail
		truncated = true
	}

	summary = limitLen(summary, g.opts.SummaryMaxLen)
	tokenCost := estimateTextTokens(payload)

	res.Payload = payload
	res.Summary = summary
	res.Truncated = truncated
	res.TokenCost = tokenCost
	return res
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func defaultSummary(payload string) string {
	if payload == "" {
		return "empty result"
	}
	return limitLen(oneLine(payload), 160)
}

func limitLen(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func estimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	const charsPerToken = 4
	return (len(s) + charsPerToken - 1) / charsPerToken
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
