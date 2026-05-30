package promptasm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/promptasm"
)

func TestAssemblerOrdersStableToVolatileAndMarksCache(t *testing.T) {
	out, err := promptasm.New().Assemble(context.Background(), promptasm.Input{
		Model:        "m",
		System:       []domain.Message{{Role: domain.RoleSystem, Content: "system"}},
		Tools:        []domain.ToolSpec{{Name: "zeta", Description: "last"}, {Name: "alpha", Description: "first"}},
		RepoMap:      "main.go\n  func Run()",
		History:      []domain.Message{{Role: domain.RoleAssistant, Content: "history"}},
		WorkingFiles: []string{"file body"},
		LatestUser:   domain.Message{Role: domain.RoleUser, Content: "latest"},
		FreshTool:    []domain.Message{{Role: domain.RoleTool, ToolResults: []domain.ToolResult{{ToolCallID: "1", Content: "fresh"}}}},
		PromptCache:  true,
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got := renderRoles(out.Request.Messages)
	want := "system:system\nsystem:tools:\n- alpha: first\n- zeta: last\nsystem:repo map:\nmain.go\n  func Run()\nassistant:history\nsystem:working file:\nfile body\nuser:latest\ntool:\n"
	if got != want {
		t.Fatalf("messages:\n%s", got)
	}
	if out.StablePrefixHash == "" {
		t.Fatal("missing stable prefix hash")
	}
	if out.StablePrefix[len(out.StablePrefix)-1].CacheControl != "ephemeral" {
		t.Fatalf("cache control = %+v", out.StablePrefix)
	}
	if out.Request.KeepAlive != "10m" {
		t.Fatalf("keep alive = %q", out.Request.KeepAlive)
	}
}

func TestStablePrefixBytesIgnoreVolatileChanges(t *testing.T) {
	asm := promptasm.New()
	base := promptasm.Input{
		System:      []domain.Message{{Role: domain.RoleSystem, Content: "system"}},
		RepoMap:     "repo",
		History:     []domain.Message{{Role: domain.RoleAssistant, Content: "history one"}},
		LatestUser:  domain.Message{Role: domain.RoleUser, Content: "latest one"},
		PromptCache: true,
	}
	first, err := asm.Assemble(context.Background(), base)
	if err != nil {
		t.Fatalf("first Assemble: %v", err)
	}
	base.History = []domain.Message{{Role: domain.RoleAssistant, Content: "history two"}}
	base.LatestUser = domain.Message{Role: domain.RoleUser, Content: "latest two"}
	second, err := asm.Assemble(context.Background(), base)
	if err != nil {
		t.Fatalf("second Assemble: %v", err)
	}
	if first.StablePrefixHash != second.StablePrefixHash {
		t.Fatalf("stable prefix changed: %s != %s", first.StablePrefixHash, second.StablePrefixHash)
	}
	if string(promptasm.StablePrefixBytes(first.StablePrefix)) != string(promptasm.StablePrefixBytes(second.StablePrefix)) {
		t.Fatal("stable prefix bytes changed")
	}
}

func TestSQLiteCacheIsContentAddressed(t *testing.T) {
	cache, err := promptasm.OpenMemoryCache()
	if err != nil {
		t.Fatalf("OpenMemoryCache: %v", err)
	}
	defer cache.Close()

	key := promptasm.CacheKey("summary", "same body")
	err = cache.Put(context.Background(), promptasm.CacheEntry{Key: key, Kind: "summary", Value: "compact"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := cache.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got.Value != "compact" || got.Hash == "" {
		t.Fatalf("entry = %+v ok=%v", got, ok)
	}
	if other := promptasm.CacheKey("summary", "changed body"); other == key {
		t.Fatal("cache key did not change with content")
	}
}

func renderRoles(messages []domain.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(string(msg.Role))
		b.WriteByte(':')
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}
