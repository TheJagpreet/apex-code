package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/provider/ollama"
)

type Features struct {
	Sessions  bool `toml:"sessions"`
	Telemetry bool `toml:"telemetry"`
	MCP       bool `toml:"mcp"`
}

type MCPServer struct {
	Name    string            `toml:"name"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	Enabled bool              `toml:"enabled"`
}

type Settings struct {
	Model         string
	BaseURL       string
	MaxIterations int
	LazyTools     bool
	SkillRoots    []string
	DataDir       string
	StateDBPath   string
	Resume        string
	Budget        agent.BudgetFractions
	Features      Features
	MCPServers    []MCPServer

	ProjectConfigPath string
	UserConfigPath    string
}

type Partial struct {
	Model         *string
	BaseURL       *string
	MaxIterations *int
	LazyTools     *bool
	SkillRoots    []string
	DataDir       *string
	StateDBPath   *string
	Resume        *string
	Budget        *BudgetPartial
	Features      *Features
	MCPServers    []MCPServer
}

type BudgetPartial struct {
	System         *float64 `toml:"system"`
	Tools          *float64 `toml:"tools"`
	History        *float64 `toml:"history"`
	Retrieved      *float64 `toml:"retrieved"`
	WorkingFiles   *float64 `toml:"working_files"`
	OutputHeadroom *float64 `toml:"output_headroom"`
}

type fileConfig struct {
	Model         string        `toml:"model"`
	BaseURL       string        `toml:"ollama_url"`
	MaxIterations int           `toml:"max_iterations"`
	LazyTools     *bool         `toml:"lazy_tools"`
	SkillRoots    []string      `toml:"skills"`
	DataDir       string        `toml:"data_dir"`
	StateDBPath   string        `toml:"state_db"`
	Resume        string        `toml:"resume"`
	Budget        BudgetPartial `toml:"budget"`
	Features      Features      `toml:"features"`
	MCPServers    []MCPServer   `toml:"mcp_servers"`
}

func Resolve(cwd string, env map[string]string, flags Partial) (Settings, error) {
	projectConfig := filepath.Join(cwd, "apex.toml")
	userDir, _ := os.UserConfigDir()
	userConfig := filepath.Join(userDir, "apex-code", "apex.toml")

	settings := defaultSettings(cwd, projectConfig, userConfig)

	for _, src := range []struct {
		path string
	}{
		{path: userConfig},
		{path: projectConfig},
	} {
		partial, err := loadPartial(src.path)
		if err != nil {
			return Settings{}, err
		}
		applyPartial(&settings, partial)
	}

	applyPartial(&settings, partialFromEnv(env))
	applyPartial(&settings, flags)

	settings.SkillRoots = cleanStrings(settings.SkillRoots)
	if settings.Model == "" {
		settings.Model = ollama.DefaultModel
	}
	if settings.BaseURL == "" {
		settings.BaseURL = ollama.DefaultBaseURL
	}
	if settings.MaxIterations <= 0 {
		settings.MaxIterations = 50
	}
	if settings.DataDir == "" {
		settings.DataDir = filepath.Join(cwd, ".apex")
	}
	if settings.StateDBPath == "" {
		settings.StateDBPath = filepath.Join(settings.DataDir, "apex.db")
	}
	return settings, nil
}

func defaultSettings(cwd, projectConfig, userConfig string) Settings {
	return Settings{
		Model:             ollama.DefaultModel,
		BaseURL:           ollama.DefaultBaseURL,
		MaxIterations:     50,
		LazyTools:         false,
		SkillRoots:        []string{filepath.Join(cwd, ".apex", "skills")},
		DataDir:           filepath.Join(cwd, ".apex"),
		StateDBPath:       filepath.Join(cwd, ".apex", "apex.db"),
		Budget:            agent.DefaultBudgetFractions(),
		Features:          Features{Sessions: true, Telemetry: true, MCP: true},
		ProjectConfigPath: projectConfig,
		UserConfigPath:    userConfig,
	}
}

func loadPartial(path string) (Partial, error) {
	var out Partial
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	if info.IsDir() {
		return out, fmt.Errorf("config path is a directory: %s", path)
	}
	var cfg fileConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return out, fmt.Errorf("decode %s: %w", path, err)
	}
	baseDir := filepath.Dir(path)
	return Partial{
		Model:         stringPtr(cfg.Model),
		BaseURL:       stringPtr(cfg.BaseURL),
		MaxIterations: intPtr(cfg.MaxIterations),
		LazyTools:     cfg.LazyTools,
		SkillRoots:    resolvePaths(baseDir, cfg.SkillRoots),
		DataDir:       pathPtr(baseDir, cfg.DataDir),
		StateDBPath:   pathPtr(baseDir, cfg.StateDBPath),
		Resume:        stringPtr(cfg.Resume),
		Budget:        budgetPtr(cfg.Budget),
		Features:      &cfg.Features,
		MCPServers:    cfg.MCPServers,
	}, nil
}

func partialFromEnv(env map[string]string) Partial {
	out := Partial{}
	out.Model = stringPtr(env["APEX_MODEL"])
	out.BaseURL = stringPtr(env["APEX_OLLAMA_URL"])
	out.Resume = stringPtr(env["APEX_RESUME"])
	if v, ok := parseInt(env["APEX_MAX_ITERATIONS"]); ok {
		out.MaxIterations = &v
	}
	if v, ok := parseBool(env["APEX_LAZY_TOOLS"]); ok {
		out.LazyTools = &v
	}
	if v := env["APEX_SKILLS_DIR"]; strings.TrimSpace(v) != "" {
		out.SkillRoots = splitPaths(v)
	}
	out.DataDir = stringPtr(env["APEX_DATA_DIR"])
	out.StateDBPath = stringPtr(env["APEX_STATE_DB"])
	if bp := budgetFromEnv(env); bp != nil {
		out.Budget = bp
	}
	return out
}

func applyPartial(settings *Settings, partial Partial) {
	if partial.Model != nil && strings.TrimSpace(*partial.Model) != "" {
		settings.Model = strings.TrimSpace(*partial.Model)
	}
	if partial.BaseURL != nil && strings.TrimSpace(*partial.BaseURL) != "" {
		settings.BaseURL = strings.TrimSpace(*partial.BaseURL)
	}
	if partial.MaxIterations != nil && *partial.MaxIterations > 0 {
		settings.MaxIterations = *partial.MaxIterations
	}
	if partial.LazyTools != nil {
		settings.LazyTools = *partial.LazyTools
	}
	if len(partial.SkillRoots) > 0 {
		settings.SkillRoots = append([]string(nil), partial.SkillRoots...)
	}
	if partial.DataDir != nil && strings.TrimSpace(*partial.DataDir) != "" {
		settings.DataDir = filepath.Clean(*partial.DataDir)
	}
	if partial.StateDBPath != nil && strings.TrimSpace(*partial.StateDBPath) != "" {
		settings.StateDBPath = filepath.Clean(*partial.StateDBPath)
	}
	if partial.Resume != nil {
		settings.Resume = strings.TrimSpace(*partial.Resume)
	}
	if partial.Budget != nil {
		applyBudget(&settings.Budget, *partial.Budget)
	}
	if partial.Features != nil {
		settings.Features = *partial.Features
	}
	if len(partial.MCPServers) > 0 {
		settings.MCPServers = append([]MCPServer(nil), partial.MCPServers...)
	}
}

func applyBudget(dst *agent.BudgetFractions, src BudgetPartial) {
	if src.System != nil {
		dst.System = *src.System
	}
	if src.Tools != nil {
		dst.Tools = *src.Tools
	}
	if src.History != nil {
		dst.History = *src.History
	}
	if src.Retrieved != nil {
		dst.Retrieved = *src.Retrieved
	}
	if src.WorkingFiles != nil {
		dst.WorkingFiles = *src.WorkingFiles
	}
	if src.OutputHeadroom != nil {
		dst.OutputHeadroom = *src.OutputHeadroom
	}
}

func budgetPtr(src BudgetPartial) *BudgetPartial {
	if src == (BudgetPartial{}) {
		return nil
	}
	cp := src
	return &cp
}

func budgetFromEnv(env map[string]string) *BudgetPartial {
	out := BudgetPartial{}
	found := false
	for _, spec := range []struct {
		key string
		set func(float64)
	}{
		{"APEX_BUDGET_SYSTEM", func(v float64) { out.System = &v }},
		{"APEX_BUDGET_TOOLS", func(v float64) { out.Tools = &v }},
		{"APEX_BUDGET_HISTORY", func(v float64) { out.History = &v }},
		{"APEX_BUDGET_RETRIEVED", func(v float64) { out.Retrieved = &v }},
		{"APEX_BUDGET_WORKING_FILES", func(v float64) { out.WorkingFiles = &v }},
		{"APEX_BUDGET_OUTPUT_HEADROOM", func(v float64) { out.OutputHeadroom = &v }},
	} {
		if v, ok := parseFloat(env[spec.key]); ok {
			spec.set(v)
			found = true
		}
	}
	if !found {
		return nil
	}
	return &out
}

func resolvePaths(baseDir string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		out = append(out, filepath.Clean(path))
	}
	return out
}

func pathPtr(baseDir, path string) *string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)
	return &path
}

func cleanStrings(in []string) []string {
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, filepath.Clean(item))
		}
	}
	return out
}

func splitPaths(s string) []string {
	var out []string
	for _, item := range strings.Split(s, string(os.PathListSeparator)) {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, filepath.Clean(item))
		}
	}
	return out
}

func parseInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func parseBool(s string) (bool, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return false, false
	}
	v, err := strconv.ParseBool(s)
	return v, err == nil
}

func stringPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}

func intPtr(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
