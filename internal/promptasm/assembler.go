package promptasm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/apex-code/apex/internal/domain"
)

func (a *Assembler) Assemble(_ context.Context, in Input) (Output, error) {
	if a == nil {
		a = New()
	}
	if in.LatestUser.Role == "" && in.LatestUser.Content != "" {
		in.LatestUser.Role = domain.RoleUser
	}

	messages := make([]domain.Message, 0)
	stableEnd := 0

	messages = append(messages, cloneMessages(in.System)...)
	stableEnd = len(messages)

	if len(in.Tools) > 0 {
		messages = append(messages, domain.Message{
			Role:    domain.RoleSystem,
			Content: renderToolDescriptors(in.Tools, a.ToolDescriptorLimit),
		})
		stableEnd = len(messages)
	}

	if strings.TrimSpace(in.RepoMap) != "" {
		messages = append(messages, domain.Message{
			Role:    domain.RoleSystem,
			Content: "repo map:\n" + strings.TrimSpace(in.RepoMap),
		})
		stableEnd = len(messages)
	}

	stablePrefix := cloneMessages(messages[:stableEnd])
	if in.PromptCache && len(messages) > 0 && stableEnd > 0 {
		messages[stableEnd-1].CacheControl = "ephemeral"
		stablePrefix[len(stablePrefix)-1].CacheControl = "ephemeral"
	}

	messages = append(messages, cloneMessages(in.History)...)
	for _, body := range in.WorkingFiles {
		if strings.TrimSpace(body) == "" {
			continue
		}
		messages = append(messages, domain.Message{
			Role:    domain.RoleSystem,
			Content: "working file:\n" + strings.TrimSpace(body),
		})
	}
	if in.LatestUser.Role != "" || in.LatestUser.Content != "" {
		messages = append(messages, cloneMessage(in.LatestUser))
	}
	messages = append(messages, cloneMessages(in.FreshTool)...)

	keepAlive := in.KeepAlive
	if keepAlive == "" {
		keepAlive = a.DefaultKeepAlive
	}

	req := domain.Request{
		Model:       in.Model,
		Messages:    messages,
		Tools:       sortedToolSpecs(in.Tools),
		Temperature: in.Temperature,
		MaxTokens:   in.MaxTokens,
		Stop:        append([]string(nil), in.Stop...),
		KeepAlive:   keepAlive,
	}

	return Output{
		Request:          req,
		StablePrefixHash: HashMessages(stablePrefix),
		StablePrefix:     stablePrefix,
	}, nil
}

func renderToolDescriptors(tools []domain.ToolSpec, limit int) string {
	sorted := sortedToolSpecs(tools)
	lines := make([]string, 0, len(sorted)+1)
	lines = append(lines, "tools:")
	for _, tool := range sorted {
		desc := compact(tool.Description, limit)
		if desc == "" {
			desc = "no description"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", tool.Name, desc))
	}
	return strings.Join(lines, "\n")
}

func sortedToolSpecs(tools []domain.ToolSpec) []domain.ToolSpec {
	out := make([]domain.ToolSpec, 0, len(tools))
	for _, tool := range tools {
		out = append(out, domain.ToolSpec{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  append([]byte(nil), tool.Parameters...),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func HashMessages(messages []domain.Message) string {
	body, _ := json.Marshal(messages)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func StablePrefixBytes(messages []domain.Message) []byte {
	body, _ := json.Marshal(messages)
	return body
}

func cloneMessages(messages []domain.Message) []domain.Message {
	out := make([]domain.Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, cloneMessage(msg))
	}
	return out
}

func cloneMessage(msg domain.Message) domain.Message {
	return domain.Message{
		Role:         msg.Role,
		Content:      msg.Content,
		ToolCalls:    append([]domain.ToolCall(nil), msg.ToolCalls...),
		ToolResults:  append([]domain.ToolResult(nil), msg.ToolResults...),
		CacheControl: msg.CacheControl,
	}
}

func compact(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit > 0 && len(text) > limit {
		return strings.TrimSpace(text[:limit]) + "..."
	}
	return text
}
