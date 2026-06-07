package agents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apex-code/apex/internal/agents"
)

func TestDiscoverAndLoadMarkdownAgents(t *testing.T) {
	root := t.TempDir()
	body := `---
name: frontend
description: focus on UI and interaction polish
aliases:
  - ui
  - ux
---
You are the frontend specialist for this repository.`
	if err := os.WriteFile(filepath.Join(root, "frontend.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := agents.NewLoader(root)
	if err := loader.Discover(); err != nil {
		t.Fatal(err)
	}
	hdrs := loader.Headers()
	if len(hdrs) != 1 || hdrs[0].Name != "frontend" {
		t.Fatalf("unexpected headers: %+v", hdrs)
	}
	agent, err := loader.Load("ui")
	if err != nil {
		t.Fatal(err)
	}
	if agent.File() != "frontend.md" || agent.Prompt == "" {
		t.Fatalf("unexpected agent: %+v", agent)
	}
}
