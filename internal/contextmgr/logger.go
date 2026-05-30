package contextmgr

import "log/slog"

type SlogInstrumenter struct {
	Logger *slog.Logger
}

func (s SlogInstrumenter) Render(report RenderReport) {
	if s.Logger == nil {
		return
	}
	s.Logger.Info("context render",
		slog.Int("context_window", report.ContextWindow),
		slog.Int("prompt_limit", report.PromptLimit),
		slog.Int("tokens_in", report.TokensIn),
		slog.Int("tokens_out", report.TokensOut),
		slog.Int("tokens_saved", report.TokensSaved),
		slog.Any("tokens_by_pool", report.TokensByPool),
		slog.Any("saved_by", report.SavedBy),
		slog.Any("evicted", report.Evicted),
		slog.Any("digested", report.Digested),
		slog.Any("summarized", report.Summarized),
		slog.Any("elided", report.Elided),
	)
}

type MultiInstrumenter []Instrumenter

func (m MultiInstrumenter) Render(report RenderReport) {
	for _, inst := range m {
		if inst != nil {
			inst.Render(report)
		}
	}
}
