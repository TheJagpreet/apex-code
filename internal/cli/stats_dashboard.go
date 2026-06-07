package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/session"
	"github.com/apex-code/apex/internal/telemetry"
)

type dashboardView struct {
	GeneratedAt      string
	ScopeLabel       string
	DataDir          string
	Overview         dashboardOverview
	Sessions         []dashboardSession
	Models           []dashboardModel
	Tools            []dashboardTool
	Agents           []dashboardAgent
	CustomAgents     []dashboardExtension
	CustomSkills     []dashboardExtension
	RecentLLM        []telemetry.TurnMetric
	OverviewJSON     template.JS
	SessionsJSON     template.JS
	ModelsJSON       template.JS
	ToolsJSON        template.JS
	AgentsJSON       template.JS
	CustomAgentsJSON template.JS
	CustomSkillsJSON template.JS
	RecentLLMJSON    template.JS
}

type dashboardOverview struct {
	SessionCount       int
	LLMCalls           int
	ToolRuns           int
	WorkflowCount      int
	CustomAgentCount   int
	CustomSkillCount   int
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
	AvgLatencyMs       int64
	CacheHitRatio      float64
	FirstAt            int64
	LastAt             int64
	Models             []string
	Modes              []string
	RecoverableToolErr int
	ToolErr            int
}

type dashboardSession struct {
	ID               string
	Title            string
	Model            string
	CWDBase          string
	LastUpdated      time.Time
	FirstSeen        time.Time
	LLMCalls         int
	ToolRuns         int
	WorkflowCount    int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	AvgLatencyMs     int64
	Modes            []string
}

type dashboardModel struct {
	Name             string
	LLMCalls         int
	SessionCount     int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	AvgLatencyMs     int64
}

type dashboardTool struct {
	Name        string
	Calls       int
	Errors      int
	Recoverable int
	DurationMs  int64
}

type dashboardAgent struct {
	Name         string
	LLMCalls     int
	ToolRuns     int
	WorkflowRuns int
	TotalTokens  int
}

type dashboardExtension struct {
	Name         string
	Files        []string
	SessionCount int
	LLMCalls     int
	ToolRuns     int
	TotalTokens  int
}

func openStatsDashboard(dataDir, sessionID string, includeModels, includeSessions bool, traceLimit int) int {
	ctx := context.Background()
	store, err := telemetry.Open(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	defer store.Close()
	sessStore, err := session.Open(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	defer sessStore.Close()
	report, err := buildStatsReport(ctx, store, sessStore, sessionID, traceLimit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	path, err := writeStatsDashboard(dataDir, report, sessionID, includeModels, includeSessions)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	if err := openBrowser(path); err != nil {
		fmt.Fprintln(os.Stderr, "apex:", err)
		return 1
	}
	fmt.Printf("Opened stats dashboard: %s\n", path)
	return 0
}

func writeStatsDashboard(dataDir string, report statsReport, sessionID string, includeModels, includeSessions bool) (string, error) {
	outputDir := filepath.Join(filepath.Clean(dataDir), "stats")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	view, err := newDashboardView(dataDir, report, sessionID, includeModels, includeSessions)
	if err != nil {
		return "", err
	}
	path := filepath.Join(outputDir, "index.html")
	var buf bytes.Buffer
	if err := statsDashboardTemplate.Execute(&buf, view); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func newDashboardView(dataDir string, report statsReport, sessionID string, includeModels, includeSessions bool) (dashboardView, error) {
	scopeLabel := "All sessions"
	if strings.TrimSpace(sessionID) != "" {
		scopeLabel = "Session " + sessionID
	}
	view := dashboardView{
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		ScopeLabel:  scopeLabel,
		DataDir:     filepath.Clean(dataDir),
		Overview: dashboardOverview{
			SessionCount:       report.overview.sessionCount,
			LLMCalls:           report.overview.llmCalls,
			ToolRuns:           report.overview.toolRuns,
			WorkflowCount:      report.overview.workflowCount,
			CustomAgentCount:   report.overview.customAgentCount,
			CustomSkillCount:   report.overview.customSkillCount,
			PromptTokens:       report.overview.promptTokens,
			CompletionTokens:   report.overview.completionTokens,
			TotalTokens:        report.overview.totalTokens,
			AvgLatencyMs:       report.overview.avgLatencyMs,
			CacheHitRatio:      report.overview.cacheHitRatio,
			FirstAt:            report.overview.firstAt,
			LastAt:             report.overview.lastAt,
			Models:             append([]string(nil), report.overview.models...),
			Modes:              append([]string(nil), report.overview.modes...),
			RecoverableToolErr: report.overview.recoverableToolErr,
			ToolErr:            report.overview.toolErr,
		},
		RecentLLM: report.recentLLM,
	}
	for _, sess := range report.sessions {
		view.Sessions = append(view.Sessions, dashboardSession{
			ID:               sess.id,
			Title:            sess.title,
			Model:            sess.model,
			CWDBase:          sess.cwdBase,
			LastUpdated:      sess.lastUpdated,
			FirstSeen:        sess.firstSeen,
			LLMCalls:         sess.llmCalls,
			ToolRuns:         sess.toolRuns,
			WorkflowCount:    sess.workflowCount,
			PromptTokens:     sess.promptTokens,
			CompletionTokens: sess.completionTokens,
			TotalTokens:      sess.totalTokens,
			AvgLatencyMs:     sess.avgLatencyMs,
			Modes:            append([]string(nil), sess.modes...),
		})
	}
	for _, model := range report.models {
		view.Models = append(view.Models, dashboardModel{
			Name:             model.name,
			LLMCalls:         model.llmCalls,
			SessionCount:     model.sessionCount,
			PromptTokens:     model.promptTokens,
			CompletionTokens: model.completionTokens,
			TotalTokens:      model.totalTokens,
			AvgLatencyMs:     model.avgLatencyMs,
		})
	}
	for _, tool := range report.tools {
		view.Tools = append(view.Tools, dashboardTool{
			Name:        tool.name,
			Calls:       tool.calls,
			Errors:      tool.errors,
			Recoverable: tool.recoverable,
			DurationMs:  tool.durationMs,
		})
	}
	for _, agent := range report.agents {
		view.Agents = append(view.Agents, dashboardAgent{
			Name:         agent.name,
			LLMCalls:     agent.llmCalls,
			ToolRuns:     agent.toolRuns,
			WorkflowRuns: agent.workflowRuns,
			TotalTokens:  agent.totalTokens,
		})
	}
	for _, item := range report.customAgents {
		view.CustomAgents = append(view.CustomAgents, dashboardExtension{
			Name:         item.name,
			Files:        append([]string(nil), item.files...),
			SessionCount: len(item.sessionIDs),
			LLMCalls:     item.llmCalls,
			ToolRuns:     item.toolRuns,
			TotalTokens:  item.totalTokens,
		})
	}
	for _, item := range report.customSkills {
		view.CustomSkills = append(view.CustomSkills, dashboardExtension{
			Name:         item.name,
			Files:        append([]string(nil), item.files...),
			SessionCount: len(item.sessionIDs),
			LLMCalls:     item.llmCalls,
			ToolRuns:     item.toolRuns,
			TotalTokens:  item.totalTokens,
		})
	}
	if !includeModels {
		view.Models = nil
	}
	if !includeSessions && len(view.Sessions) > 6 {
		view.Sessions = view.Sessions[:6]
	}
	var err error
	if view.OverviewJSON, err = mustJSON(view.Overview); err != nil {
		return dashboardView{}, err
	}
	if view.SessionsJSON, err = mustJSON(view.Sessions); err != nil {
		return dashboardView{}, err
	}
	if view.ModelsJSON, err = mustJSON(view.Models); err != nil {
		return dashboardView{}, err
	}
	if view.ToolsJSON, err = mustJSON(view.Tools); err != nil {
		return dashboardView{}, err
	}
	if view.AgentsJSON, err = mustJSON(view.Agents); err != nil {
		return dashboardView{}, err
	}
	if view.CustomAgentsJSON, err = mustJSON(view.CustomAgents); err != nil {
		return dashboardView{}, err
	}
	if view.CustomSkillsJSON, err = mustJSON(view.CustomSkills); err != nil {
		return dashboardView{}, err
	}
	if view.RecentLLMJSON, err = mustJSON(view.RecentLLM); err != nil {
		return dashboardView{}, err
	}
	return view, nil
}

func mustJSON(v any) (template.JS, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return template.JS(data), nil
}

func openBrowser(path string) error {
	if strings.EqualFold(os.Getenv("APEX_NO_OPEN_BROWSER"), "1") {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", "", abs).Run()
	case "darwin":
		return exec.Command("open", abs).Run()
	default:
		return exec.Command("xdg-open", abs).Run()
	}
}

var statsDashboardTemplate = template.Must(template.New("stats-dashboard").Funcs(template.FuncMap{
	"shortID":          shortID,
	"formatInt":        formatInt,
	"formatDurationMs": formatDurationMs,
	"formatTimeAgo":    formatTimeAgo,
	"formatTime": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04:05")
	},
	"join": func(items []string) string {
		if len(items) == 0 {
			return "-"
		}
		return strings.Join(items, ", ")
	},
	"avgLatencySession": func(s dashboardSession) string {
		return formatDurationMs(s.AvgLatencyMs)
	},
	"avgLatencyModel": func(m dashboardModel) string {
		return formatDurationMs(avgOrZero(m.AvgLatencyMs, m.LLMCalls))
	},
	"avgLatencyTool": func(t dashboardTool) string {
		return formatDurationMs(avgOrZero(t.DurationMs, t.Calls))
	},
	"ts": func(v int64) string {
		return formatUnix(v)
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>apex stats</title>
  <style>
    :root {
      --bg: #090909;
      --panel: rgba(19, 20, 24, 0.9);
      --panel-2: rgba(28, 30, 36, 0.85);
      --line: rgba(255,255,255,0.08);
      --text: #f4f1e8;
      --muted: #a8a39a;
      --gold: #d7b67a;
      --gold-2: #f0d7a4;
      --red: #d67171;
      --green: #87c8a3;
      --shadow: 0 24px 80px rgba(0,0,0,0.45);
      --radius: 24px;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Segoe UI", "Helvetica Neue", Arial, sans-serif;
      color: var(--text);
      background:
        radial-gradient(circle at top left, rgba(215,182,122,0.12), transparent 28%),
        radial-gradient(circle at top right, rgba(240,215,164,0.08), transparent 24%),
        linear-gradient(180deg, #0b0b0c 0%, #060607 100%);
      min-height: 100vh;
    }
    .shell {
      width: min(1440px, calc(100% - 48px));
      margin: 24px auto 40px;
    }
    .hero {
      position: relative;
      overflow: hidden;
      padding: 28px 32px;
      border: 1px solid var(--line);
      border-radius: calc(var(--radius) + 8px);
      background:
        linear-gradient(135deg, rgba(255,255,255,0.02), rgba(255,255,255,0)),
        linear-gradient(180deg, rgba(20,20,24,0.95), rgba(10,10,12,0.95));
      box-shadow: var(--shadow);
    }
    .hero::after {
      content: "";
      position: absolute;
      inset: auto -10% -30% auto;
      width: 360px;
      height: 360px;
      background: radial-gradient(circle, rgba(215,182,122,0.18), transparent 60%);
      pointer-events: none;
    }
    .eyebrow {
      color: var(--gold);
      text-transform: uppercase;
      letter-spacing: 0.22em;
      font-size: 12px;
      margin-bottom: 12px;
    }
    h1 {
      margin: 0;
      font-size: clamp(34px, 4vw, 56px);
      font-weight: 650;
      letter-spacing: -0.04em;
    }
    .subtitle {
      margin-top: 10px;
      color: var(--muted);
      font-size: 15px;
      max-width: 900px;
      line-height: 1.6;
    }
    .hero-meta {
      display: flex;
      flex-wrap: wrap;
      gap: 12px;
      margin-top: 20px;
    }
    .chip {
      border: 1px solid rgba(215,182,122,0.2);
      color: var(--gold-2);
      background: rgba(215,182,122,0.08);
      border-radius: 999px;
      padding: 8px 12px;
      font-size: 13px;
    }
    .section {
      margin-top: 22px;
      padding: 22px;
      border: 1px solid var(--line);
      border-radius: var(--radius);
      background: var(--panel);
      box-shadow: var(--shadow);
      backdrop-filter: blur(18px);
    }
    .section h2 {
      margin: 0 0 18px;
      font-size: 18px;
      font-weight: 600;
      letter-spacing: 0.02em;
    }
    .cards {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
      gap: 14px;
    }
    .card {
      padding: 16px;
      border-radius: 18px;
      border: 1px solid var(--line);
      background: linear-gradient(180deg, rgba(255,255,255,0.03), rgba(255,255,255,0.01));
    }
    .label {
      color: var(--muted);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.12em;
      margin-bottom: 10px;
    }
    .value {
      font-size: 28px;
      font-weight: 650;
      letter-spacing: -0.03em;
    }
    .hint {
      margin-top: 8px;
      color: var(--muted);
      font-size: 13px;
    }
    .grid-2 {
      display: grid;
      grid-template-columns: 1.15fr 0.85fr;
      gap: 22px;
    }
    table {
      width: 100%;
      border-collapse: collapse;
      overflow: hidden;
      border-radius: 16px;
      background: rgba(255,255,255,0.015);
    }
    th, td {
      padding: 12px 14px;
      border-bottom: 1px solid var(--line);
      text-align: left;
      vertical-align: top;
      font-size: 14px;
    }
    th {
      color: var(--gold-2);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.12em;
      background: rgba(255,255,255,0.02);
    }
    tr:last-child td { border-bottom: none; }
    .mono {
      font-family: "Cascadia Code", "Consolas", monospace;
      font-size: 13px;
    }
    .muted { color: var(--muted); }
    .pill {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      padding: 6px 10px;
      border-radius: 999px;
      background: rgba(255,255,255,0.04);
      border: 1px solid var(--line);
      font-size: 12px;
      color: var(--muted);
    }
    .accent { color: var(--gold-2); }
    .danger { color: var(--red); }
    .good { color: var(--green); }
    .footer {
      margin-top: 18px;
      color: var(--muted);
      font-size: 12px;
      text-align: right;
    }
    @media (max-width: 980px) {
      .grid-2 { grid-template-columns: 1fr; }
      .shell { width: min(100% - 24px, 1440px); }
    }
  </style>
</head>
<body>
  <div class="shell">
    <section class="hero">
      <div class="eyebrow">Apex Telemetry</div>
      <h1>Session Intelligence Dashboard</h1>
      <div class="subtitle">A polished readout of everything apex can infer from your <span class="mono">.apex/sessions</span> artifact tree: sessions, workflows, coder agents, custom agents, custom skills, tokens, tools, latency, and recent model activity.</div>
      <div class="hero-meta">
        <div class="chip">{{ .ScopeLabel }}</div>
        <div class="chip">Data root: {{ .DataDir }}</div>
        <div class="chip">Generated: {{ .GeneratedAt }}</div>
      </div>
    </section>

    <section class="section">
      <h2>Overview</h2>
      <div class="cards">
        <div class="card"><div class="label">Sessions</div><div class="value">{{ formatInt .Overview.SessionCount }}</div><div class="hint">Artifacts discovered</div></div>
        <div class="card"><div class="label">LLM Calls</div><div class="value">{{ formatInt .Overview.LLMCalls }}</div><div class="hint">Prompt/response turns</div></div>
        <div class="card"><div class="label">Tool Runs</div><div class="value">{{ formatInt .Overview.ToolRuns }}</div><div class="hint">Standalone tool executions</div></div>
        <div class="card"><div class="label">Workflows</div><div class="value">{{ formatInt .Overview.WorkflowCount }}</div><div class="hint">Coder workflows observed</div></div>
        <div class="card"><div class="label">Custom Agents</div><div class="value">{{ formatInt .Overview.CustomAgentCount }}</div><div class="hint">Unique custom agent bundles used</div></div>
        <div class="card"><div class="label">Custom Skills</div><div class="value">{{ formatInt .Overview.CustomSkillCount }}</div><div class="hint">Unique custom skill bundles used</div></div>
        <div class="card"><div class="label">Total Tokens</div><div class="value">{{ formatInt .Overview.TotalTokens }}</div><div class="hint">{{ formatInt .Overview.PromptTokens }} prompt / {{ formatInt .Overview.CompletionTokens }} completion</div></div>
        <div class="card"><div class="label">Average Latency</div><div class="value">{{ formatDurationMs .Overview.AvgLatencyMs }}</div><div class="hint">Across all LLM calls</div></div>
        <div class="card"><div class="label">Models</div><div class="value accent">{{ join .Overview.Models }}</div><div class="hint">Observed across sessions</div></div>
        <div class="card"><div class="label">Modes</div><div class="value accent">{{ join .Overview.Modes }}</div><div class="hint">{{ ts .Overview.FirstAt }} → {{ ts .Overview.LastAt }}</div></div>
      </div>
    </section>

    <div class="grid-2">
      <section class="section">
        <h2>Sessions</h2>
        <table>
          <thead>
            <tr>
              <th>Session</th>
              <th>Mode</th>
              <th>LLM</th>
              <th>Tools</th>
              <th>Tokens</th>
              <th>Workflows</th>
              <th>Updated</th>
              <th>Title</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Sessions }}
            <tr>
              <td class="mono">{{ shortID .ID }}</td>
              <td>{{ join .Modes }}</td>
              <td>{{ formatInt .LLMCalls }}</td>
              <td>{{ formatInt .ToolRuns }}</td>
              <td>{{ formatInt .TotalTokens }}</td>
              <td>{{ formatInt .WorkflowCount }}</td>
              <td>{{ formatTimeAgo .LastUpdated }}</td>
              <td>{{ if .Title }}{{ .Title }}{{ else }}<span class="muted">(untitled)</span>{{ end }}</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </section>

      <section class="section">
        <h2>Tool Activity</h2>
        <table>
          <thead>
            <tr>
              <th>Tool</th>
              <th>Calls</th>
              <th>Errors</th>
              <th>Recoverable</th>
              <th>Average</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Tools }}
            <tr>
              <td class="mono">{{ .Name }}</td>
              <td>{{ formatInt .Calls }}</td>
              <td class="{{ if gt .Errors 0 }}danger{{ else }}muted{{ end }}">{{ formatInt .Errors }}</td>
              <td class="{{ if gt .Recoverable 0 }}good{{ else }}muted{{ end }}">{{ formatInt .Recoverable }}</td>
              <td>{{ avgLatencyTool . }}</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </section>
    </div>

    <div class="grid-2">
      <section class="section">
        <h2>Model Usage</h2>
        <table>
          <thead>
            <tr>
              <th>Model</th>
              <th>Sessions</th>
              <th>LLM</th>
              <th>Prompt</th>
              <th>Completion</th>
              <th>Total</th>
              <th>Average</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Models }}
            <tr>
              <td>{{ .Name }}</td>
              <td>{{ formatInt .SessionCount }}</td>
              <td>{{ formatInt .LLMCalls }}</td>
              <td>{{ formatInt .PromptTokens }}</td>
              <td>{{ formatInt .CompletionTokens }}</td>
              <td>{{ formatInt .TotalTokens }}</td>
              <td>{{ avgLatencyModel . }}</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </section>

      <section class="section">
        <h2>Coder Agents</h2>
        <table>
          <thead>
            <tr>
              <th>Agent</th>
              <th>LLM</th>
              <th>Tools</th>
              <th>Workflow events</th>
              <th>Tokens</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Agents }}
            <tr>
              <td>{{ .Name }}</td>
              <td>{{ formatInt .LLMCalls }}</td>
              <td>{{ formatInt .ToolRuns }}</td>
              <td>{{ formatInt .WorkflowRuns }}</td>
              <td>{{ formatInt .TotalTokens }}</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </section>
    </div>

    <div class="grid-2">
      <section class="section">
        <h2>Custom Agents</h2>
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Files</th>
              <th>Sessions</th>
              <th>LLM</th>
              <th>Tools</th>
              <th>Tokens</th>
            </tr>
          </thead>
          <tbody>
            {{ range .CustomAgents }}
            <tr>
              <td>{{ .Name }}</td>
              <td class="mono">{{ join .Files }}</td>
              <td>{{ formatInt .SessionCount }}</td>
              <td>{{ formatInt .LLMCalls }}</td>
              <td>{{ formatInt .ToolRuns }}</td>
              <td>{{ formatInt .TotalTokens }}</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </section>

      <section class="section">
        <h2>Custom Skills</h2>
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Files</th>
              <th>Sessions</th>
              <th>LLM</th>
              <th>Tools</th>
              <th>Tokens</th>
            </tr>
          </thead>
          <tbody>
            {{ range .CustomSkills }}
            <tr>
              <td>{{ .Name }}</td>
              <td class="mono">{{ join .Files }}</td>
              <td>{{ formatInt .SessionCount }}</td>
              <td>{{ formatInt .LLMCalls }}</td>
              <td>{{ formatInt .ToolRuns }}</td>
              <td>{{ formatInt .TotalTokens }}</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </section>
    </div>

    <section class="section">
      <h2>Recent LLM Calls</h2>
      <table>
        <thead>
          <tr>
            <th>When</th>
            <th>Session</th>
            <th>Turn</th>
            <th>Model</th>
            <th>Total tokens</th>
            <th>Latency</th>
            <th>Termination</th>
          </tr>
        </thead>
        <tbody>
          {{ range .RecentLLM }}
          <tr>
            <td>{{ ts .CreatedAt }}</td>
            <td class="mono">{{ shortID .SessionID }}</td>
            <td>{{ formatInt .TurnIndex }}</td>
            <td>{{ .Model }}</td>
            <td>{{ formatInt .TotalTokens }}</td>
            <td>{{ formatDurationMs .DurationMs }}</td>
            <td>{{ if .Termination }}{{ .Termination }}{{ else }}<span class="muted">-</span>{{ end }}</td>
          </tr>
          {{ end }}
        </tbody>
      </table>
      <div class="footer">apex-code stats dashboard • rendered from local session artifacts</div>
    </section>
  </div>
  <script>
    window.APEX_STATS = {
      overview: {{ .OverviewJSON }},
      sessions: {{ .SessionsJSON }},
      models: {{ .ModelsJSON }},
      tools: {{ .ToolsJSON }},
      agents: {{ .AgentsJSON }},
      customAgents: {{ .CustomAgentsJSON }},
      customSkills: {{ .CustomSkillsJSON }},
      recentLLM: {{ .RecentLLMJSON }}
    };
  </script>
</body>
</html>`))
