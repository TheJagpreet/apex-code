package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apex-code/apex/internal/config"
)

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	projectToml := filepath.Join(dir, "apex.toml")
	if err := os.WriteFile(projectToml, []byte(`
model = "project-model"
ollama_url = "http://project"
max_iterations = 4
lazy_tools = true
skills = ["skills/project"]
data_dir = ".apex-project"
state_db = ".apex-project/state.db"

[budget]
history = 0.25
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	model := "flag-model"
	lazy := false
	maxIterations := 9
	settings, err := config.Resolve(dir, map[string]string{
		"APEX_OLLAMA_URL":     "http://env",
		"APEX_MAX_ITERATIONS": "7",
	}, config.Partial{
		Model:         &model,
		LazyTools:     &lazy,
		MaxIterations: &maxIterations,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if settings.Model != "flag-model" {
		t.Fatalf("model = %q", settings.Model)
	}
	if settings.BaseURL != "http://env" {
		t.Fatalf("base url = %q", settings.BaseURL)
	}
	if settings.MaxIterations != 9 {
		t.Fatalf("max iterations = %d", settings.MaxIterations)
	}
	if settings.LazyTools {
		t.Fatal("flag override should disable lazy tools")
	}
	if want := filepath.Join(dir, ".apex-project", "state.db"); settings.StateDBPath != want {
		t.Fatalf("state db = %q, want %q", settings.StateDBPath, want)
	}
	if len(settings.SkillRoots) != 1 || settings.SkillRoots[0] != filepath.Join(dir, "skills", "project") {
		t.Fatalf("skill roots = %v", settings.SkillRoots)
	}
	if settings.Budget.History != 0.25 {
		t.Fatalf("budget history = %f", settings.Budget.History)
	}
}
