package session_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider/fake"
	"github.com/apex-code/apex/internal/session"
)

func TestSessionRoundTripAndRehydrate(t *testing.T) {
	store, err := session.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	mgr := contextmgr.New(fake.New(nil), contextmgr.Options{})
	ws := contextmgr.WorkingSet{
		Items: []contextmgr.Item{
			{
				Meta: contextmgr.Metadata{
					ID:       "message:1",
					Kind:     contextmgr.ItemHistory,
					Source:   contextmgr.SourceMessage,
					LastUsed: time.Unix(10, 0),
				},
				Message: domain.Message{Role: domain.RoleUser, Content: "hello"},
			},
			{
				Meta: contextmgr.Metadata{
					ID:       "retrieved:" + path,
					Kind:     contextmgr.ItemRetrieved,
					Source:   contextmgr.SourceFile,
					Path:     path,
					Hash:     hashText("hello"),
					LastUsed: time.Unix(20, 0),
				},
				Message: domain.Message{Role: domain.RoleSystem, Content: "retrieved context:\nhello"},
			},
		},
	}

	record, snap, err := saveFixture(context.Background(), store, ws)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if record.ID == "" || snap.Version != 1 {
		t.Fatalf("record=%+v snapshot=%+v", record, snap)
	}

	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatalf("mutate fixture: %v", err)
	}

	gotRecord, loaded, turns, err := store.Load(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if gotRecord.ID != record.ID || len(turns) != 1 {
		t.Fatalf("loaded record=%+v turns=%+v", gotRecord, turns)
	}

	rehydrated := mgr.Rehydrate(loaded.WorkingSet)
	msgs := mgr.Messages(rehydrated)
	if len(msgs) != 1 {
		t.Fatalf("rehydrated messages = %d, want stale file item dropped", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Fatalf("rehydrated content = %q", msgs[0].Content)
	}
}

func TestSessionLoadLatest(t *testing.T) {
	store, err := session.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()

	mgr := contextmgr.New(fake.New(nil), contextmgr.Options{})
	ws := mgr.FromMessages([]domain.Message{{Role: domain.RoleUser, Content: "first"}})
	if _, _, err := saveFixture(context.Background(), store, ws); err != nil {
		t.Fatalf("save first: %v", err)
	}
	time.Sleep(time.Millisecond)
	ws = mgr.FromMessages([]domain.Message{{Role: domain.RoleUser, Content: "second"}})
	second, _, err := saveFixture(context.Background(), store, ws)
	if err != nil {
		t.Fatalf("save second: %v", err)
	}

	record, snap, _, err := store.Load(context.Background(), "latest")
	if err != nil {
		t.Fatalf("load latest: %v", err)
	}
	if record.ID != second.ID || snap.Model != "gemma4:e2b" {
		t.Fatalf("latest=%+v snapshot=%+v", record, snap)
	}
}

func TestResumedPromptStaysCompact(t *testing.T) {
	store, err := session.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()

	raw := []domain.Message{
		{Role: domain.RoleUser, Content: "please inspect the service layer and explain the failure in detail"},
		{Role: domain.RoleAssistant, Content: "I inspected service.go and found two retry branches plus one timeout path"},
		{Role: domain.RoleUser, Content: "summarize the key findings and next fix"},
	}
	compact := contextmgr.WorkingSet{
		Items: []contextmgr.Item{
			{
				Meta:    contextmgr.Metadata{ID: "system:summary", Kind: contextmgr.ItemSummary, Source: contextmgr.SourceSummary, LastUsed: time.Unix(1, 0)},
				Message: domain.Message{Role: domain.RoleUser, Content: "story so far: service retries twice, timeout path missing wrapped error"},
			},
			{
				Meta:    contextmgr.Metadata{ID: "latest:user", Kind: contextmgr.ItemHistory, Source: contextmgr.SourceMessage, LastUsed: time.Unix(2, 0), Pinned: true},
				Message: domain.Message{Role: domain.RoleUser, Content: "summarize the key findings and next fix"},
			},
		},
	}
	if _, _, err := saveFixture(context.Background(), store, compact); err != nil {
		t.Fatalf("save: %v", err)
	}

	mgr := contextmgr.New(fake.New(nil), contextmgr.Options{})
	_, snap, _, err := store.Load(context.Background(), "latest")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resumed := mgr.Messages(mgr.Rehydrate(snap.WorkingSet))

	provider := fake.New(nil)
	rawTokens, err := provider.CountTokens(context.Background(), raw)
	if err != nil {
		t.Fatalf("raw tokens: %v", err)
	}
	resumedTokens, err := provider.CountTokens(context.Background(), resumed)
	if err != nil {
		t.Fatalf("resumed tokens: %v", err)
	}
	if resumedTokens >= rawTokens {
		t.Fatalf("resumed prompt should stay compact: resumed=%d raw=%d", resumedTokens, rawTokens)
	}
}

func TestSessionStoreUsesDotPrefixedDataDirAsDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".apex")
	store, err := session.Open(root)
	if err != nil {
		t.Fatalf("open session store: %v", err)
	}
	if _, _, err := saveFixture(context.Background(), store, contextmgr.WorkingSet{}); err != nil {
		t.Fatalf("save fixture: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sessions")); err != nil {
		t.Fatalf("expected sessions dir under dot-prefixed data dir: %v", err)
	}
}

func TestSessionListSkipsFoldersWithoutSessionDocument(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(root)
	if err != nil {
		t.Fatalf("open session store: %v", err)
	}
	if _, _, err := saveFixture(context.Background(), store, contextmgr.WorkingSet{}); err != nil {
		t.Fatalf("save fixture: %v", err)
	}
	extraDir := filepath.Join(root, "sessions", "telemetry-only")
	if err := os.MkdirAll(extraDir, 0o755); err != nil {
		t.Fatalf("mkdir extra dir: %v", err)
	}
	records, err := store.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected only resumable sessions, got %d", len(records))
	}
}

func saveFixture(ctx context.Context, store *session.Store, ws contextmgr.WorkingSet) (session.Record, session.Snapshot, error) {
	return store.Save(ctx, session.SaveInput{
		Model:       "gemma4:e2b",
		CWD:         "repo",
		Prompt:      "continue coding",
		Termination: "final_answer",
		Snapshot: session.Snapshot{
			Model:      "gemma4:e2b",
			CWD:        "repo",
			WorkingSet: ws,
		},
		Turns: []session.TurnRecord{{Index: 1, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, StopReason: "end_turn"}},
	})
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
