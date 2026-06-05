package cli

import (
	"fmt"
	"strings"

	"github.com/apex-code/apex/internal/provider"
	"github.com/apex-code/apex/internal/provider/ollama"
	"github.com/apex-code/apex/internal/provider/openai"
)

func newProvider(cfg Config) (provider.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", "ollama":
		return ollama.New(ollama.WithModel(cfg.Model), ollama.WithBaseURL(cfg.BaseURL)), nil
	case "openai":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, fmt.Errorf("openai provider requires OPENAI_API_KEY or APEX_API_KEY")
		}
		return openai.New(
			openai.WithModel(cfg.Model),
			openai.WithBaseURL(cfg.BaseURL),
			openai.WithAPIKey(cfg.APIKey),
		), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q (supported: ollama, openai)", cfg.Provider)
	}
}
