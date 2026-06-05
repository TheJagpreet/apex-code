package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/apex-code/apex/internal/config"
	"github.com/apex-code/apex/internal/mcp"
	"github.com/apex-code/apex/internal/telemetry"
	"github.com/apex-code/apex/internal/tui"
)

// Mode is how apex was invoked.
type Mode int

const (
	// ModeInteractive launches the Bubble Tea TUI/REPL (plan 9.4–9.9).
	ModeInteractive Mode = iota
	// ModeOneShot runs a single prompt from the command line (plan 9.2).
	ModeOneShot
	// ModePipe reads the prompt/context from stdin (plan 9.3).
	ModePipe
)

func (m Mode) String() string {
	switch m {
	case ModeOneShot:
		return "one-shot"
	case ModePipe:
		return "pipe"
	default:
		return "interactive"
	}
}

// Main parses flags, picks an invocation mode, and runs apex. It returns a
// process exit code.
func Main(args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "stats":
			return runStats(args[1:])
		case "sessions":
			return runSessions(args[1:])
		case "mcp":
			return runMCP(args[1:])
		}
	}

	fs := flag.NewFlagSet("apex", flag.ContinueOnError)
	var (
		provider   = fs.String("provider", "ollama", "model provider (ollama or openai)")
		model      = fs.String("model", "", "provider model name")
		baseURL    = fs.String("base-url", "", "provider base URL")
		ollamaURL  = fs.String("ollama-url", "", "deprecated alias for -base-url when using Ollama")
		maxIter    = fs.Int("max-iterations", 50, "maximum agent loop turns before stopping")
		verbose    = fs.Bool("verbose", false, "show expanded technical details in the TUI")
		lazyTools  = fs.Bool("lazy-tools", false, "advertise tool/skill names only; inject full schemas on demand (plan 8)")
		skillsDir  = fs.String("skills", defaultSkillsDir(), "directory of skill bundles")
		dataDir    = fs.String("data-dir", "", "base directory for sessions, workflows, and telemetry")
		stateDB    = fs.String("state-db", "", "deprecated compatibility alias for the state path inside the data directory")
		resume     = fs.String("resume", "", "resume a prior session by id or 'latest'")
		forceTUI   = fs.Bool("tui", false, "force the interactive TUI even with a prompt argument")
		forceShell = fs.Bool("one-shot", false, "force one-shot mode (never launch the TUI)")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cwd, _ := os.Getwd()
	resolvedBaseURL := strings.TrimSpace(*baseURL)
	if resolvedBaseURL == "" {
		resolvedBaseURL = strings.TrimSpace(*ollamaURL)
	}
	settings, err := config.Resolve(cwd, envMap(cwd), config.Partial{
		Provider:      nonDefaultString(*provider, "ollama"),
		Model:         nonEmptyString(*model),
		BaseURL:       nonEmptyString(resolvedBaseURL),
		MaxIterations: nonDefaultInt(*maxIter, 50),
		LazyTools:     nonDefaultBool(*lazyTools, false),
		SkillRoots:    nonDefaultRoots(*skillsDir, defaultSkillsDir()),
		DataDir:       nonEmptyString(*dataDir),
		StateDBPath:   nonEmptyString(*stateDB),
		Resume:        nonEmptyString(*resume),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}

	cfg := Config{
		Provider:      settings.Provider,
		Model:         settings.Model,
		BaseURL:       settings.BaseURL,
		APIKey:        settings.APIKey,
		MaxIterations: settings.MaxIterations,
		LazyTools:     settings.LazyTools,
		SkillRoots:    settings.SkillRoots,
		Budget:        settings.Budget,
		BudgetSet:     settings.BudgetSet,
		CWD:           cwd,
		DataDir:       settings.DataDir,
		StateDBPath:   settings.StateDBPath,
		Resume:        settings.Resume,
		Features:      settings.Features,
		MCPServers:    settings.MCPServers,
	}

	argPrompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	piped := stdinIsPiped(os.Stdin)

	mode := pickMode(argPrompt, piped, *forceTUI, *forceShell)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch mode {
	case ModePipe:
		stdinText := readAll(os.Stdin)
		cfg.Prompt = strings.TrimSpace(joinPrompt(argPrompt, stdinText))
	case ModeOneShot:
		cfg.Prompt = argPrompt
	}

	if mode != ModeInteractive && cfg.Prompt == "" {
		fmt.Fprintln(os.Stderr, `apex: usage: apex [flags] "your prompt"  (or pipe input on stdin)`)
		return 1
	}

	deps, err := BuildDeps(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	defer deps.Close()

	if cfg.Resume != "" {
		if err := deps.LoadResume(ctx, cfg.Resume); err != nil {
			fmt.Fprintln(os.Stderr, "apex:", err)
			return 1
		}
	}

	switch mode {
	case ModeInteractive:
		if err := tui.Run(ctx, newTUIAgent(ctx, deps), *verbose); err != nil {
			fmt.Fprintln(os.Stderr, "apex:", err)
			return 1
		}
		return 0
	default:
		if err := runOnceToStdout(ctx, deps, cfg.Prompt); err != nil {
			fmt.Fprintln(os.Stderr, "apex:", err)
			return 1
		}
		return 0
	}
}

func runOnceToStdout(ctx context.Context, deps *Deps, prompt string) error {
	if err := deps.EnsureModel(ctx); err != nil {
		return err
	}
	state, err := deps.RunOnce(ctx, prompt)
	if err != nil {
		return err
	}
	if state.FinalResponse == nil {
		return fmt.Errorf("agent loop ended without a final response (reason=%s)", state.TerminationReason)
	}
	fmt.Print(state.FinalResponse.Message.Content)
	fmt.Fprintf(os.Stderr, "\n\n[loop] turns=%d termination=%s\n", len(state.Turns), state.TerminationReason)
	fmt.Fprintf(os.Stderr, "[usage] prompt=%d completion=%d total=%d (stop=%s)\n",
		state.FinalResponse.Usage.PromptTokens,
		state.FinalResponse.Usage.CompletionTokens,
		state.FinalResponse.Usage.TotalTokens,
		state.FinalResponse.StopReason,
	)
	if deps.Telemetry != nil {
		if totals, err := deps.Telemetry.Totals(ctx, deps.effectiveSessionID()); err == nil {
			fmt.Fprintf(os.Stderr, "[stats] %s\n", telemetry.FormatTotals(totals))
		}
	}
	return nil
}

// pickMode resolves the invocation mode from inputs and overrides. Exported
// indirectly via Main; kept small and pure so it is unit-testable.
func pickMode(argPrompt string, piped, forceTUI, forceShell bool) Mode {
	switch {
	case forceTUI:
		return ModeInteractive
	case forceShell:
		return ModeOneShot
	case piped:
		return ModePipe
	case argPrompt != "":
		return ModeOneShot
	default:
		return ModeInteractive
	}
}

func joinPrompt(arg, stdin string) string {
	switch {
	case arg == "":
		return stdin
	case stdin == "":
		return arg
	default:
		return arg + "\n\n" + stdin
	}
}

func stdinIsPiped(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) == 0
}

func readAll(r io.Reader) string {
	data, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return string(data)
}

func splitRoots(s string) []string {
	var out []string
	for _, p := range strings.Split(s, string(os.PathListSeparator)) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runStats(args []string) int {
	fs := flag.NewFlagSet("apex stats", flag.ContinueOnError)
	var (
		dataDir   = fs.String("data-dir", "", "base directory for sessions, workflows, and telemetry")
		stateDB   = fs.String("state-db", "", "deprecated compatibility alias for the state path inside the data directory")
		session   = fs.String("session", "", "optional session id to scope the stats")
		byModel   = fs.Bool("by-model", false, "break usage down per model")
		bySession = fs.Bool("by-session", false, "break usage down per session id")
		trace     = fs.Int("trace", 0, "show the N most recent turn-level traces")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cwd, _ := os.Getwd()
	settings, err := config.Resolve(cwd, envMap(cwd), config.Partial{
		DataDir:     nonEmptyString(*dataDir),
		StateDBPath: nonEmptyString(*stateDB),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	store, err := telemetry.Open(settings.StateDBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	defer store.Close()

	ctx := context.Background()
	sessionID := strings.TrimSpace(*session)

	total, err := store.Totals(ctx, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	fmt.Println(telemetry.FormatTotals(total))

	if *byModel {
		rollup, err := store.ByModel(ctx, sessionID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "apex:", err)
			return 1
		}
		fmt.Println("\n[by model]")
		fmt.Println(telemetry.FormatByModel(rollup))
	}
	if *bySession {
		rollup, err := store.BySession(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "apex:", err)
			return 1
		}
		fmt.Println("\n[by session]")
		fmt.Println(telemetry.FormatByModel(rollup))
	}
	if *trace > 0 {
		traces, err := store.Recent(ctx, sessionID, *trace)
		if err != nil {
			fmt.Fprintln(os.Stderr, "apex:", err)
			return 1
		}
		fmt.Println("\n[recent traces]")
		fmt.Println(telemetry.FormatTrace(traces))
	}
	return 0
}

func runSessions(args []string) int {
	fs := flag.NewFlagSet("apex sessions", flag.ContinueOnError)
	var (
		dataDir = fs.String("data-dir", "", "base directory for sessions, workflows, and telemetry")
		stateDB = fs.String("state-db", "", "deprecated compatibility alias for the state path inside the data directory")
		limit   = fs.Int("limit", 10, "maximum sessions to list")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cwd, _ := os.Getwd()
	settings, err := config.Resolve(cwd, envMap(cwd), config.Partial{
		DataDir:     nonEmptyString(*dataDir),
		StateDBPath: nonEmptyString(*stateDB),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	deps, err := BuildDeps(Config{StateDBPath: settings.StateDBPath, Features: settings.Features})
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	defer deps.Close()
	if deps.Sessions == nil {
		fmt.Fprintln(os.Stderr, "apex: sessions are disabled")
		return 1
	}
	records, err := deps.Sessions.List(context.Background(), *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	for _, rec := range records {
		fmt.Printf("%s  %s  %s  turns=%d  %s\n", rec.ID, rec.Model, filepath.Base(rec.CWD), rec.TurnCount, rec.Title)
	}
	return 0
}

func runMCP(args []string) int {
	fs := flag.NewFlagSet("apex mcp", flag.ContinueOnError)
	var serverName string
	fs.StringVar(&serverName, "server", "", "configured MCP server name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cwd, _ := os.Getwd()
	settings, err := config.Resolve(cwd, envMap(cwd), config.Partial{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	server, ok := pickMCPServer(settings.MCPServers, serverName)
	if !ok {
		fmt.Fprintln(os.Stderr, "apex: MCP server not found; configure one in apex.toml and pass -server if needed")
		return 1
	}
	client, err := mcp.NewClient(mcp.Config{
		Name:    server.Name,
		Command: server.Command,
		Args:    server.Args,
		Env:     server.Env,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	defer client.Close()
	ctx := context.Background()
	tools, err := client.ListTools(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	resources, err := client.ListResources(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	fmt.Printf("server: %s\n", server.Name)
	fmt.Println("tools:")
	for _, tool := range tools {
		fmt.Printf("- %s: %s\n", tool.Name, tool.Description)
	}
	fmt.Println("resources:")
	for _, resource := range resources {
		fmt.Printf("- %s (%s)\n", resource.Name, resource.URI)
	}
	return 0
}

func defaultSkillsDir() string {
	if v := os.Getenv("APEX_SKILLS_DIR"); v != "" {
		return v
	}
	return ".apex/skills"
}

var errNoPrompt = errors.New("no prompt provided")

func envMap(cwd string) map[string]string {
	out := map[string]string{}
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			out[key] = value
		}
	}
	for key, value := range loadDotEnv(filepath.Join(cwd, ".env")) {
		if _, exists := out[key]; !exists {
			out[key] = value
		}
	}
	return out
}

func nonEmptyString(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	v = strings.TrimSpace(v)
	return &v
}

func nonDefaultString(v, def string) *string {
	if v == def {
		return nil
	}
	return nonEmptyString(v)
}

func nonDefaultInt(v, def int) *int {
	if v == def {
		return nil
	}
	return &v
}

func nonDefaultBool(v, def bool) *bool {
	if v == def {
		return nil
	}
	return &v
}

func nonDefaultRoots(v, def string) []string {
	if strings.TrimSpace(v) == strings.TrimSpace(def) {
		return nil
	}
	return splitRoots(v)
}

func pickMCPServer(servers []config.MCPServer, name string) (config.MCPServer, bool) {
	name = strings.TrimSpace(name)
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		if name == "" || server.Name == name {
			return server, true
		}
	}
	return config.MCPServer{}, false
}
