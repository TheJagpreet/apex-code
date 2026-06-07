package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apex-code/apex/internal/session"
	"github.com/apex-code/apex/internal/telemetry"
)

type statsReport struct {
	overview     overviewStats
	sessions     []sessionStats
	models       []modelStats
	tools        []toolStats
	agents       []agentStats
	customAgents []extensionStats
	customSkills []extensionStats
	recentLLM    []telemetry.TurnMetric
}

type overviewStats struct {
	sessionCount       int
	llmCalls           int
	toolRuns           int
	workflowCount      int
	customAgentCount   int
	customSkillCount   int
	promptTokens       int
	completionTokens   int
	totalTokens        int
	avgLatencyMs       int64
	cacheHitRatio      float64
	firstAt            int64
	lastAt             int64
	models             []string
	modes              []string
	recoverableToolErr int
	toolErr            int
}

type sessionStats struct {
	id               string
	title            string
	model            string
	cwdBase          string
	lastUpdated      time.Time
	firstSeen        time.Time
	llmCalls         int
	toolRuns         int
	workflowCount    int
	promptTokens     int
	completionTokens int
	totalTokens      int
	avgLatencyMs     int64
	modes            []string
}

type modelStats struct {
	name             string
	llmCalls         int
	sessionCount     int
	promptTokens     int
	completionTokens int
	totalTokens      int
	avgLatencyMs     int64
}

type toolStats struct {
	name        string
	calls       int
	errors      int
	recoverable int
	durationMs  int64
}

type agentStats struct {
	name         string
	llmCalls     int
	toolRuns     int
	workflowRuns int
	totalTokens  int
}

type extensionStats struct {
	name        string
	files       []string
	sessionIDs  map[string]bool
	llmCalls    int
	toolRuns    int
	totalTokens int
}

func buildStatsReport(ctx context.Context, tele *telemetry.Store, sess *session.Store, sessionID string, traceLimit int) (statsReport, error) {
	artifacts, err := tele.Artifacts(ctx, sessionID)
	if err != nil {
		return statsReport{}, err
	}
	report := statsReport{}
	modelIndex := map[string]*modelStats{}
	toolIndex := map[string]*toolStats{}
	agentIndex := map[string]*agentStats{}
	customAgentIndex := map[string]*extensionStats{}
	customSkillIndex := map[string]*extensionStats{}
	recordIndex := map[string]session.Record{}
	workflowSeen := map[string]bool{}
	overviewModels := map[string]bool{}
	overviewModes := map[string]bool{}

	if sess != nil {
		records, err := sess.List(ctx, 0)
		if err != nil {
			return statsReport{}, err
		}
		for _, rec := range records {
			recordIndex[rec.ID] = rec
		}
	}

	for _, artifact := range artifacts {
		stats := sessionStats{
			id:          artifact.SessionID,
			model:       artifact.Model,
			cwdBase:     filepath.Base(strings.TrimSpace(artifact.CWD)),
			lastUpdated: artifact.UpdatedAt,
			firstSeen:   artifact.CreatedAt,
		}
		if rec, ok := recordIndex[artifact.SessionID]; ok {
			stats.title = rec.Title
			if strings.TrimSpace(stats.model) == "" {
				stats.model = rec.Model
			}
			if strings.TrimSpace(stats.cwdBase) == "" {
				stats.cwdBase = filepath.Base(rec.CWD)
			}
			if rec.UpdatedAt > 0 {
				stats.lastUpdated = time.Unix(0, rec.UpdatedAt)
			}
			if rec.CreatedAt > 0 {
				stats.firstSeen = time.Unix(0, rec.CreatedAt)
			}
		}

		modeSet := map[string]bool{}
		workflowSet := map[string]bool{}
		for _, event := range artifact.Events {
			if strings.TrimSpace(event.Mode) != "" {
				modeSet[event.Mode] = true
				overviewModes[event.Mode] = true
			}
			if strings.TrimSpace(event.Model) != "" {
				overviewModels[event.Model] = true
			}
			if strings.TrimSpace(event.WorkflowID) != "" {
				workflowSet[event.WorkflowID] = true
				workflowSeen[event.WorkflowID] = true
			}
			switch event.Kind {
			case "llm_turn", "stage_llm", "task_llm_turn":
				stats.llmCalls++
				stats.promptTokens += event.PromptTokens
				stats.completionTokens += event.CompletionTokens
				stats.totalTokens += event.TotalTokens
				stats.avgLatencyMs += event.DurationMs

				report.overview.llmCalls++
				report.overview.promptTokens += event.PromptTokens
				report.overview.completionTokens += event.CompletionTokens
				report.overview.totalTokens += event.TotalTokens
				report.overview.avgLatencyMs += event.DurationMs
				report.overview.cacheHitRatio += float64(event.CacheRead)
				report.overview.firstAt = minTimestamp(report.overview.firstAt, event.Timestamp.Unix())
				report.overview.lastAt = maxTimestamp(report.overview.lastAt, event.Timestamp.Unix())

				modelName := firstNonBlank(event.Model, stats.model, "(unknown)")
				ms := modelIndex[modelName]
				if ms == nil {
					ms = &modelStats{name: modelName}
					modelIndex[modelName] = ms
				}
				ms.llmCalls++
				ms.promptTokens += event.PromptTokens
				ms.completionTokens += event.CompletionTokens
				ms.totalTokens += event.TotalTokens
				ms.avgLatencyMs += event.DurationMs

				if strings.TrimSpace(event.Agent) != "" {
					as := agentIndex[event.Agent]
					if as == nil {
						as = &agentStats{name: event.Agent}
						agentIndex[event.Agent] = as
					}
					as.llmCalls++
					as.totalTokens += event.TotalTokens
					if strings.TrimSpace(event.WorkflowID) != "" {
						as.workflowRuns++
					}
				}
				accumulateExtensionUsage(customAgentIndex, event.CustomAgent, event.CustomAgentFile, artifact.SessionID, event.TotalTokens, true)
				for i, name := range event.CustomSkills {
					file := ""
					if i < len(event.CustomSkillFiles) {
						file = event.CustomSkillFiles[i]
					}
					accumulateExtensionUsage(customSkillIndex, name, file, artifact.SessionID, event.TotalTokens, true)
				}
			case "tool_exec":
				stats.toolRuns++
				report.overview.toolRuns++
				if event.Recoverable {
					report.overview.recoverableToolErr++
				}
				if strings.EqualFold(event.Outcome, "error") {
					report.overview.toolErr++
				}
				toolName := firstToolName(event)
				if toolName != "" {
					ts := toolIndex[toolName]
					if ts == nil {
						ts = &toolStats{name: toolName}
						toolIndex[toolName] = ts
					}
					ts.calls++
					ts.durationMs += event.DurationMs
					if event.Recoverable {
						ts.recoverable++
					}
					if strings.EqualFold(event.Outcome, "error") {
						ts.errors++
					}
				}
				if strings.TrimSpace(event.Agent) != "" {
					as := agentIndex[event.Agent]
					if as == nil {
						as = &agentStats{name: event.Agent}
						agentIndex[event.Agent] = as
					}
					as.toolRuns++
					if strings.TrimSpace(event.WorkflowID) != "" {
						as.workflowRuns++
					}
				}
				accumulateExtensionUsage(customAgentIndex, event.CustomAgent, event.CustomAgentFile, artifact.SessionID, 0, false)
				for i, name := range event.CustomSkills {
					file := ""
					if i < len(event.CustomSkillFiles) {
						file = event.CustomSkillFiles[i]
					}
					accumulateExtensionUsage(customSkillIndex, name, file, artifact.SessionID, 0, false)
				}
			}
		}
		if stats.llmCalls > 0 {
			stats.avgLatencyMs /= int64(stats.llmCalls)
		}
		stats.workflowCount = len(workflowSet)
		stats.modes = sortedKeys(modeSet)
		report.sessions = append(report.sessions, stats)
	}

	report.overview.sessionCount = len(report.sessions)
	report.overview.workflowCount = len(workflowSeen)
	report.overview.models = sortedKeys(overviewModels)
	report.overview.modes = sortedKeys(overviewModes)
	if report.overview.llmCalls > 0 {
		report.overview.avgLatencyMs /= int64(report.overview.llmCalls)
		totalCache := 0
		totalRead := 0
		for _, artifact := range artifacts {
			for _, event := range artifact.Events {
				totalCache += event.CacheCreation + event.CacheRead
				totalRead += event.CacheRead
			}
		}
		if totalCache > 0 {
			report.overview.cacheHitRatio = float64(totalRead) / float64(totalCache)
		} else {
			report.overview.cacheHitRatio = 0
		}
	}

	for _, ss := range report.sessions {
		if ss.model == "" {
			continue
		}
		ms := modelIndex[ss.model]
		if ms == nil {
			ms = &modelStats{name: ss.model}
			modelIndex[ss.model] = ms
		}
		ms.sessionCount++
	}
	report.models = collectModelStats(modelIndex)
	report.tools = collectToolStats(toolIndex)
	report.agents = collectAgentStats(agentIndex)
	report.customAgents = collectExtensionStats(customAgentIndex)
	report.customSkills = collectExtensionStats(customSkillIndex)
	report.overview.customAgentCount = len(report.customAgents)
	report.overview.customSkillCount = len(report.customSkills)

	sort.SliceStable(report.sessions, func(i, j int) bool {
		return report.sessions[i].lastUpdated.After(report.sessions[j].lastUpdated)
	})
	metrics, err := tele.Recent(ctx, sessionID, traceLimit)
	if err != nil {
		return statsReport{}, err
	}
	report.recentLLM = metrics
	return report, nil
}

func renderStatsReport(report statsReport, includeModels, includeSessions bool) string {
	var b strings.Builder
	b.WriteString(renderStatsBox("apex stats", []string{
		fmt.Sprintf("sessions      %d", report.overview.sessionCount),
		fmt.Sprintf("llm calls      %d", report.overview.llmCalls),
		fmt.Sprintf("tool runs      %d", report.overview.toolRuns),
		fmt.Sprintf("workflows      %d", report.overview.workflowCount),
		fmt.Sprintf("custom agents  %d", report.overview.customAgentCount),
		fmt.Sprintf("custom skills  %d", report.overview.customSkillCount),
		fmt.Sprintf("tokens         %s prompt / %s completion / %s total", formatInt(report.overview.promptTokens), formatInt(report.overview.completionTokens), formatInt(report.overview.totalTokens)),
		fmt.Sprintf("latency        %s avg", formatDurationMs(report.overview.avgLatencyMs)),
		fmt.Sprintf("cache hit      %.1f%%", report.overview.cacheHitRatio*100),
		fmt.Sprintf("window         %s -> %s", formatUnix(report.overview.firstAt), formatUnix(report.overview.lastAt)),
		fmt.Sprintf("models         %s", renderList(report.overview.models, "(none)")),
		fmt.Sprintf("modes          %s", renderList(report.overview.modes, "(none)")),
		fmt.Sprintf("tool errors    %d hard / %d recoverable", report.overview.toolErr, report.overview.recoverableToolErr),
	}))
	b.WriteString("\n\n")
	b.WriteString(renderSessionsTable(report.sessions, includeSessions))
	if includeModels {
		b.WriteString("\n\n")
		b.WriteString(renderModelsTable(report.models))
	}
	b.WriteString("\n\n")
	b.WriteString(renderToolsTable(report.tools))
	if len(report.agents) > 0 {
		b.WriteString("\n\n")
		b.WriteString(renderAgentsTable(report.agents))
	}
	if len(report.customAgents) > 0 {
		b.WriteString("\n\n")
		b.WriteString(renderExtensionsTable("Custom Agents", report.customAgents))
	}
	if len(report.customSkills) > 0 {
		b.WriteString("\n\n")
		b.WriteString(renderExtensionsTable("Custom Skills", report.customSkills))
	}
	if len(report.recentLLM) > 0 {
		b.WriteString("\n\n")
		b.WriteString(renderRecentTable(report.recentLLM))
	}
	return b.String()
}

func renderStatsBox(title string, lines []string) string {
	width := len(title) + 4
	for _, line := range lines {
		if l := len(line) + 2; l > width {
			width = l
		}
	}
	var b strings.Builder
	b.WriteString("┌")
	b.WriteString(strings.Repeat("─", width))
	b.WriteString("┐\n")
	b.WriteString("│ ")
	b.WriteString(padRight(title, width-1))
	b.WriteString("│\n")
	b.WriteString("├")
	b.WriteString(strings.Repeat("─", width))
	b.WriteString("┤\n")
	for _, line := range lines {
		b.WriteString("│ ")
		b.WriteString(padRight(line, width-1))
		b.WriteString("│\n")
	}
	b.WriteString("└")
	b.WriteString(strings.Repeat("─", width))
	b.WriteString("┘")
	return b.String()
}

func renderSessionsTable(sessions []sessionStats, expanded bool) string {
	if len(sessions) == 0 {
		return renderStatsBox("Sessions", []string{"No session telemetry found."})
	}
	limit := 6
	if expanded {
		limit = len(sessions)
	}
	headers := []string{"session", "mode", "llm", "tools", "tokens", "workflows", "updated", "title"}
	rows := make([][]string, 0, minInt(limit, len(sessions)))
	for _, sess := range sessions[:minInt(limit, len(sessions))] {
		rows = append(rows, []string{
			shortID(sess.id),
			renderList(sess.modes, "chat"),
			formatInt(sess.llmCalls),
			formatInt(sess.toolRuns),
			formatInt(sess.totalTokens),
			formatInt(sess.workflowCount),
			formatTimeAgo(sess.lastUpdated),
			firstNonBlank(sess.title, "(untitled)"),
		})
	}
	title := "Sessions"
	if !expanded && len(sessions) > limit {
		title = fmt.Sprintf("Sessions (latest %d of %d)", limit, len(sessions))
	}
	return renderTableBox(title, headers, rows)
}

func renderModelsTable(models []modelStats) string {
	if len(models) == 0 {
		return renderStatsBox("Models", []string{"No model usage found."})
	}
	headers := []string{"model", "sessions", "llm", "prompt", "completion", "total", "avg"}
	rows := make([][]string, 0, len(models))
	for _, model := range models {
		rows = append(rows, []string{
			model.name,
			formatInt(model.sessionCount),
			formatInt(model.llmCalls),
			formatInt(model.promptTokens),
			formatInt(model.completionTokens),
			formatInt(model.totalTokens),
			formatDurationMs(avgOrZero(model.avgLatencyMs, model.llmCalls)),
		})
	}
	return renderTableBox("Models", headers, rows)
}

func renderToolsTable(tools []toolStats) string {
	if len(tools) == 0 {
		return renderStatsBox("Tools", []string{"No tool activity found."})
	}
	limit := minInt(10, len(tools))
	headers := []string{"tool", "calls", "errors", "recoverable", "avg"}
	rows := make([][]string, 0, limit)
	for _, tool := range tools[:limit] {
		rows = append(rows, []string{
			tool.name,
			formatInt(tool.calls),
			formatInt(tool.errors),
			formatInt(tool.recoverable),
			formatDurationMs(avgOrZero(tool.durationMs, tool.calls)),
		})
	}
	title := "Tools"
	if len(tools) > limit {
		title = fmt.Sprintf("Tools (top %d)", limit)
	}
	return renderTableBox(title, headers, rows)
}

func renderAgentsTable(agents []agentStats) string {
	headers := []string{"agent", "llm", "tools", "workflow events", "tokens"}
	rows := make([][]string, 0, len(agents))
	for _, agent := range agents {
		rows = append(rows, []string{
			agent.name,
			formatInt(agent.llmCalls),
			formatInt(agent.toolRuns),
			formatInt(agent.workflowRuns),
			formatInt(agent.totalTokens),
		})
	}
	return renderTableBox("Coder Agents", headers, rows)
}

func renderExtensionsTable(title string, items []extensionStats) string {
	headers := []string{"name", "files", "sessions", "llm", "tools", "tokens"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{
			item.name,
			renderList(item.files, "-"),
			formatInt(len(item.sessionIDs)),
			formatInt(item.llmCalls),
			formatInt(item.toolRuns),
			formatInt(item.totalTokens),
		})
	}
	return renderTableBox(title, headers, rows)
}

func renderRecentTable(metrics []telemetry.TurnMetric) string {
	headers := []string{"when", "session", "turn", "model", "total", "latency", "term"}
	rows := make([][]string, 0, len(metrics))
	for _, metric := range metrics {
		rows = append(rows, []string{
			formatUnix(metric.CreatedAt),
			shortID(metric.SessionID),
			formatInt(metric.TurnIndex),
			metric.Model,
			formatInt(metric.TotalTokens),
			formatDurationMs(metric.DurationMs),
			firstNonBlank(metric.Termination, "-"),
		})
	}
	return renderTableBox("Recent LLM Calls", headers, rows)
}

func renderTableBox(title string, headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, col := range row {
			if len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}
	var b strings.Builder
	lineWidth := 1
	for _, width := range widths {
		lineWidth += width + 3
	}
	b.WriteString("┌")
	b.WriteString(strings.Repeat("─", lineWidth))
	b.WriteString("┐\n")
	b.WriteString("│ ")
	b.WriteString(padRight(title, lineWidth-1))
	b.WriteString("│\n")
	b.WriteString("├")
	b.WriteString(strings.Repeat("─", lineWidth))
	b.WriteString("┤\n")
	b.WriteString("│ ")
	for i, header := range headers {
		if i > 0 {
			b.WriteString(" │ ")
		}
		b.WriteString(padRight(header, widths[i]))
	}
	b.WriteString(" │\n")
	b.WriteString("├")
	b.WriteString(strings.Repeat("─", lineWidth))
	b.WriteString("┤\n")
	for _, row := range rows {
		b.WriteString("│ ")
		for i, col := range row {
			if i > 0 {
				b.WriteString(" │ ")
			}
			b.WriteString(padRight(col, widths[i]))
		}
		b.WriteString(" │\n")
	}
	b.WriteString("└")
	b.WriteString(strings.Repeat("─", lineWidth))
	b.WriteString("┘")
	return b.String()
}

func collectModelStats(index map[string]*modelStats) []modelStats {
	out := make([]modelStats, 0, len(index))
	for _, item := range index {
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].totalTokens == out[j].totalTokens {
			return out[i].name < out[j].name
		}
		return out[i].totalTokens > out[j].totalTokens
	})
	return out
}

func collectToolStats(index map[string]*toolStats) []toolStats {
	out := make([]toolStats, 0, len(index))
	for _, item := range index {
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].calls == out[j].calls {
			return out[i].name < out[j].name
		}
		return out[i].calls > out[j].calls
	})
	return out
}

func collectAgentStats(index map[string]*agentStats) []agentStats {
	out := make([]agentStats, 0, len(index))
	for _, item := range index {
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].totalTokens == out[j].totalTokens {
			return out[i].name < out[j].name
		}
		return out[i].totalTokens > out[j].totalTokens
	})
	return out
}

func collectExtensionStats(index map[string]*extensionStats) []extensionStats {
	out := make([]extensionStats, 0, len(index))
	for _, item := range index {
		copyItem := *item
		copyItem.files = append([]string(nil), item.files...)
		copyItem.sessionIDs = make(map[string]bool, len(item.sessionIDs))
		for k, v := range item.sessionIDs {
			copyItem.sessionIDs[k] = v
		}
		sort.Strings(copyItem.files)
		out = append(out, copyItem)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].totalTokens == out[j].totalTokens {
			return out[i].name < out[j].name
		}
		return out[i].totalTokens > out[j].totalTokens
	})
	return out
}

func accumulateExtensionUsage(index map[string]*extensionStats, name, file, sessionID string, totalTokens int, llm bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	item := index[name]
	if item == nil {
		item = &extensionStats{name: name, sessionIDs: map[string]bool{}}
		index[name] = item
	}
	if strings.TrimSpace(file) != "" && !containsString(item.files, file) {
		item.files = append(item.files, strings.TrimSpace(file))
	}
	if strings.TrimSpace(sessionID) != "" {
		item.sessionIDs[strings.TrimSpace(sessionID)] = true
	}
	if llm {
		item.llmCalls++
		item.totalTokens += totalTokens
	} else {
		item.toolRuns++
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func firstToolName(event telemetry.SessionEvent) string {
	if len(event.ToolCallDetails) > 0 && strings.TrimSpace(event.ToolCallDetails[0].Name) != "" {
		return strings.TrimSpace(event.ToolCallDetails[0].Name)
	}
	if len(event.ToolCalls) > 0 {
		return strings.TrimSpace(event.ToolCalls[0])
	}
	return ""
}

func sortedKeys[K comparable](m map[string]K) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func shortID(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 8 {
		return v
	}
	return v[:8] + "..."
}

func renderList(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return strings.Join(items, ",")
}

func padRight(v string, width int) string {
	if len(v) >= width {
		return v
	}
	return v + strings.Repeat(" ", width-len(v))
}

func formatInt(v int) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

func formatDurationMs(v int64) string {
	if v <= 0 {
		return "-"
	}
	d := time.Duration(v) * time.Millisecond
	if d >= time.Minute {
		return d.Round(time.Second).String()
	}
	return d.String()
}

func formatUnix(v int64) string {
	if v <= 0 {
		return "-"
	}
	return time.Unix(v, 0).Format("2006-01-02 15:04:05")
}

func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func avgOrZero(total int64, count int) int64 {
	if count <= 0 {
		return 0
	}
	return total / int64(count)
}

func minTimestamp(current, next int64) int64 {
	if next <= 0 {
		return current
	}
	if current == 0 || next < current {
		return next
	}
	return current
}

func maxTimestamp(current, next int64) int64 {
	if next > current {
		return next
	}
	return current
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
