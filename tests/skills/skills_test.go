package skills_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apex-code/apex/internal/skills"
	"github.com/apex-code/apex/internal/tools"
)

func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skills.BundleFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverIsLazyAndOrdered(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "refactor", `{"name":"refactor","description":"safely rename symbols across the repo","prompt":"BIG PROMPT BODY","tools":["grep","edit"]}`)
	writeSkill(t, root, "review", `{"name":"review","description":"review a diff for bugs","prompt":"ANOTHER BODY"}`)
	// A malformed bundle must not abort discovery of the good ones.
	writeSkill(t, root, "broken", `{not json`)

	loader := skills.NewLoader(root)
	if err := loader.Discover(); err != nil {
		t.Fatal(err)
	}

	hdrs := loader.Headers()
	if len(hdrs) != 2 {
		t.Fatalf("expected 2 discovered skills, got %d", len(hdrs))
	}
	if hdrs[0].Name != "refactor" || hdrs[1].Name != "review" {
		t.Fatalf("skills not sorted deterministically: %+v", hdrs)
	}
	// Headers must not carry the heavyweight prompt body.
	for _, h := range hdrs {
		if h.Description == "" {
			t.Fatalf("header missing trigger description: %+v", h)
		}
	}
}

func TestLoadAndMatch(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "refactor", `{"name":"refactor","description":"safely rename symbols across the repo","prompt":"BODY","tools":["grep","edit"]}`)

	loader := skills.NewLoader(root)
	if err := loader.Discover(); err != nil {
		t.Fatal(err)
	}

	sk, err := loader.Load("refactor")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Prompt != "BODY" || len(sk.Tools) != 2 {
		t.Fatalf("loaded skill body wrong: %+v", sk)
	}

	if got := loader.Match("can you rename this symbol everywhere"); !hasStr(got, "refactor") {
		t.Fatalf("trigger match failed: %v", got)
	}

	set := tools.NewLazySet(tools.NewRouter(tools.NewDefaultRegistry()))
	if _, err := loader.Activate("refactor", set); err != nil {
		t.Fatal(err)
	}
	if got := set.Active(); !hasStr(got, "grep") || !hasStr(got, "edit") {
		t.Fatalf("activating skill did not inject its tools: %v", got)
	}
}

func TestDiscoverMissingRootIsNoError(t *testing.T) {
	loader := skills.NewLoader(filepath.Join(t.TempDir(), "nope"))
	if err := loader.Discover(); err != nil {
		t.Fatalf("missing root should be tolerated: %v", err)
	}
	if len(loader.Headers()) != 0 {
		t.Fatal("expected no skills")
	}
}

func hasStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
