package tui

import (
	"strings"
	"testing"
)

func TestRenderMarkdownTableRendersBoxTable(t *testing.T) {
	src := `Improvement opportunities

| Area | Issue | Suggested Improvement |
| --- | --- | --- |
| Install | Windows instructions are vague | Add explicit run guidance after build |
| Quick start | Ollama path is not shown inline | Add an Ollama quick-start block |
`
	out := renderMarkdown(src, 72)
	for _, want := range []string{"┌", "┬", "│ Area", "Suggested Improvement", "Windows instructions are", "vague"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered markdown missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAllEntriesDoesNotRewrapRenderedTable(t *testing.T) {
	entry := transcriptEntry{
		Kind:  entryAssistant,
		Title: "reviewer",
		Body: "Here is the table.\n\n| A | B | C |\n| --- | --- | --- |\n| one | two words here | three |\n| four | five | six |",
	}
	out := renderAllEntries([]transcriptEntry{entry}, 0, false, 48)
	if strings.Contains(out, "|\n|") {
		t.Fatalf("rendered table looks like it was rewrapped as raw markdown:\n%s", out)
	}
	for _, want := range []string{"┌", "┬", "└", "reviewer"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered entry missing %q:\n%s", want, out)
		}
	}
}
