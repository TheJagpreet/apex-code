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
provider = "ollama"
model = "project-model"
base_url = "http://project"
max_iterations = 4
lazy_tools = true
skills = ["skills/project"]
data_dir = ".apex-project"

[budget]
history = 0.25
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	model := "flag-model"
	lazy := false
	maxIterations := 9
	settings, err := config.Resolve(dir, map[string]string{
		"APEX_PROVIDER":       "openai",
		"OPENAI_API_KEY":      "sk-test",
		"APEX_BASE_URL":       "http://env",
		"APEX_MAX_ITERATIONS": "7",
	}, config.Partial{
		Provider:      stringPtr("ollama"),
		Model:         &model,
		LazyTools:     &lazy,
		MaxIterations: &maxIterations,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if settings.Provider != "ollama" {
		t.Fatalf("provider = %q", settings.Provider)
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
	if want := filepath.Join(dir, ".apex-project"); settings.DataDir != want {
		t.Fatalf("data dir = %q, want %q", settings.DataDir, want)
	}
	if len(settings.SkillRoots) != 1 || settings.SkillRoots[0] != filepath.Join(dir, "skills", "project") {
		t.Fatalf("skill roots = %v", settings.SkillRoots)
	}
	if settings.Budget.History != 0.25 {
		t.Fatalf("budget history = %f", settings.Budget.History)
	}
	if !settings.BudgetSet {
		t.Fatal("budget should be marked explicit when configured")
	}
}

func TestResolveDefaultsToOpenAIWhenAPIKeyPresent(t *testing.T) {
	settings, err := config.Resolve(t.TempDir(), map[string]string{
		"OPENAI_API_KEY": "sk-test",
	}, config.Partial{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if settings.Provider != "openai" {
		t.Fatalf("provider = %q", settings.Provider)
	}
	if settings.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q", settings.Model)
	}
	if settings.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base url = %q", settings.BaseURL)
	}
	if settings.BudgetSet {
		t.Fatal("budget should not be marked explicit by default")
	}
}

func stringPtr(v string) *string { return &v }
