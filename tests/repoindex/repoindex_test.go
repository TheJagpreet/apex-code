package repoindex_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/agent"
	"github.com/apex-code/apex/internal/contextmgr"
	"github.com/apex-code/apex/internal/domain"
	"github.com/apex-code/apex/internal/provider/fake"
	"github.com/apex-code/apex/internal/repoindex"
)

func TestIndexRepoMapAndSearch(t *testing.T) {
	root := sampleRepo(t)
	store := memoryStore(t)
	defer store.Close()

	idx := repoindex.NewIndexer(root, store, nil)
	stats, err := idx.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.FilesSeen != 3 || stats.FilesIndexed != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.SymbolsIndexed < 5 {
		t.Fatalf("symbols = %+v", stats)
	}

	retriever := repoindex.NewRetriever(root, store)
	repoMap, err := retriever.RepoMap(context.Background(), repoindex.RepoMapOptions{MaxTokens: 120})
	if err != nil {
		t.Fatalf("RepoMap: %v", err)
	}
	if !strings.Contains(repoMap, "Hello") || strings.Contains(repoMap, "node_modules") {
		t.Fatalf("repo map = %q", repoMap)
	}

	outline, err := retriever.Outline(context.Background(), "Hello", 5)
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	if !strings.Contains(outline, "main.go") || strings.Contains(outline, "func Hidden") {
		t.Fatalf("outline = %q", outline)
	}
}

func TestIncrementalIndexSkipsUnchangedAndDeletesMissing(t *testing.T) {
	root := sampleRepo(t)
	store := memoryStore(t)
	defer store.Close()
	idx := repoindex.NewIndexer(root, store, nil)

	if _, err := idx.Index(context.Background()); err != nil {
		t.Fatalf("first Index: %v", err)
	}
	second, err := idx.Index(context.Background())
	if err != nil {
		t.Fatalf("second Index: %v", err)
	}
	if second.FilesSkipped != 3 || second.FilesIndexed != 0 {
		t.Fatalf("second stats = %+v", second)
	}

	if err := os.Remove(filepath.Join(root, "app.py")); err != nil {
		t.Fatal(err)
	}
	third, err := idx.Index(context.Background())
	if err != nil {
		t.Fatalf("third Index: %v", err)
	}
	if third.FilesDeleted != 1 {
		t.Fatalf("third stats = %+v", third)
	}
}

func TestRangeRetrievalAndWorkingSetPool(t *testing.T) {
	root := sampleRepo(t)
	store := memoryStore(t)
	defer store.Close()
	if _, err := repoindex.NewIndexer(root, store, nil).Index(context.Background()); err != nil {
		t.Fatalf("Index: %v", err)
	}

	retriever := repoindex.NewRetriever(root, store)
	body, err := retriever.Range(context.Background(), repoindex.RangeRequest{Path: "main.go", StartLine: 3, EndLine: 5})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if !strings.Contains(body, "3:") || strings.Contains(body, "1:") {
		t.Fatalf("range = %q", body)
	}

	mgr := contextmgr.New(fake.New(nil), contextmgr.Options{})
	ws := mgr.FromMessages([]domain.Message{{Role: domain.RoleUser, Content: "inspect repo"}})
	ws = mgr.AddRetrievedContext(ws, "repo-map", body, false)
	prompt, err := mgr.Render(context.Background(), ws, agent.Budget{
		TotalWindow: 256, PromptLimit: 220, OutputHeadroom: 36,
		Pools: map[agent.PoolName]int{
			agent.PoolSystem: 60, agent.PoolHistory: 80, agent.PoolRetrieved: 120,
			agent.PoolTools: 20, agent.PoolWorkingFiles: 20, agent.PoolOutputHeadroom: 36,
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if prompt.Report.TokensByPool[agent.PoolRetrieved] == 0 {
		t.Fatalf("retrieved pool not used: %+v", prompt.Report)
	}
}

func TestOptionalEmbeddingsAreOffByDefault(t *testing.T) {
	root := sampleRepo(t)
	store := memoryStore(t)
	defer store.Close()
	embedder := &recordingEmbedder{}

	if _, err := repoindex.NewIndexer(root, store, nil).WithEmbeddings(repoindex.EmbeddingOptions{
		Enabled:  false,
		Embedder: embedder,
	}).Index(context.Background()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if embedder.calls != 0 {
		t.Fatalf("embeddings called while disabled")
	}
}

func BenchmarkRepoMapVsNaiveFullFile(b *testing.B) {
	root := sampleRepo(b)
	store := memoryStore(b)
	defer store.Close()
	if _, err := repoindex.NewIndexer(root, store, nil).Index(context.Background()); err != nil {
		b.Fatal(err)
	}
	retriever := repoindex.NewRetriever(root, store)
	repoMap, err := retriever.RepoMap(context.Background(), repoindex.RepoMapOptions{MaxTokens: 120})
	if err != nil {
		b.Fatal(err)
	}
	naive := naiveRepoBytes(b, root)
	if len(repoMap) >= naive {
		b.Fatalf("repo map is not compact: repo_map=%d naive=%d", len(repoMap), naive)
	}
	b.ResetTimer()
	b.ReportMetric(float64(len(repoMap)), "repo_map_bytes")
	b.ReportMetric(float64(naive), "naive_bytes")
	for b.Loop() {
		if _, err := retriever.RepoMap(context.Background(), repoindex.RepoMapOptions{MaxTokens: 120}); err != nil {
			b.Fatal(err)
		}
	}
}

type testingTB interface {
	Helper()
	TempDir() string
	Fatal(args ...any)
}

func sampleRepo(t testingTB) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored.go\nnode_modules/\n")
	writeFile(t, filepath.Join(root, "ignored.go"), "package ignored\nfunc Hidden() {}\n")
	writeFile(t, filepath.Join(root, "main.go"), `package sample

// Hello returns a greeting.
func Hello(name string) string {
	message := "hi " + name
	if name == "" {
		return "hi"
	}
	for i := 0; i < 3; i++ {
		message = message + "!"
	}
	if len(message) > 20 {
		return message[:20]
	}
	return message
}

func Goodbye(name string) string {
	if name == "" {
		return "bye"
	}
	return "hi " + name
}

type Runner struct{}

func (Runner) Run() {}
`)
	writeFile(t, filepath.Join(root, "web.ts"), `export interface User { name: string; email: string; active: boolean }
export function renderUser(user: User) {
  const status = user.active ? "active" : "inactive"
  return user.name + " <" + user.email + "> " + status
}
const loadUser = async () => {
  const user = await fetch("/user")
  return user.json()
}
`)
	writeFile(t, filepath.Join(root, "app.py"), `# Job runs the queue.
def job():
    value = 0
    for item in range(10):
        value += item
    return value

class Worker:
    def run(self):
        return job()
`)
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "node_modules", "lib.js"), "function ignored() {}\n")
	return root
}

func writeFile(t testingTB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func memoryStore(t testingTB) *repoindex.Store {
	t.Helper()
	store, err := repoindex.OpenMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func naiveRepoBytes(t testingTB, root string) int {
	t.Helper()
	total := 0
	for _, name := range []string{"main.go", "web.ts", "app.py"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		total += len(body)
	}
	return total
}

type recordingEmbedder struct {
	calls int
}

func (r *recordingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	r.calls++
	return make([][]float32, len(texts)), nil
}
