package tools_test

import (
	"strings"
	"testing"

	"github.com/apex-code/apex/internal/tools"
)

func TestDescriptorsAreDeferred(t *testing.T) {
	r := tools.NewDefaultRegistry()
	descs := r.Descriptors()
	if len(descs) == 0 {
		t.Fatal("expected default tools to be registered")
	}
	for _, d := range descs {
		if d.Name == "" || d.Description == "" {
			t.Fatalf("descriptor missing name/description: %+v", d)
		}
		if strings.ContainsAny(d.Description, "\n\r") {
			t.Fatalf("descriptor description must be one line: %q", d.Description)
		}
	}
	if doc := r.DescribeDeferred(); !strings.Contains(doc, "read_file") {
		t.Fatalf("deferred catalogue missing read_file: %q", doc)
	}
}

func TestRouterResolveAndRoute(t *testing.T) {
	r := tools.NewDefaultRegistry()
	router := tools.NewRouter(r)

	specs := router.Resolve([]string{"grep", "grep", "does-not-exist", "read_file"})
	if len(specs) != 2 {
		t.Fatalf("expected 2 resolved specs (dedup + skip unknown), got %d", len(specs))
	}
	for _, s := range specs {
		if len(s.Parameters) == 0 {
			t.Fatalf("resolved spec %q must carry a full schema", s.Name)
		}
	}

	got := router.Route("please grep the repo and then read_file main.go")
	if !contains(got, "grep") || !contains(got, "read_file") {
		t.Fatalf("router failed to detect tool intent: %v", got)
	}
	if contains(router.Route("grepping is fun"), "grep") {
		t.Fatal("router matched a non-word substring")
	}
}

func TestLazySetInjection(t *testing.T) {
	r := tools.NewDefaultRegistry()
	set := tools.NewLazySet(tools.NewRouter(r))

	if len(set.Specs()) != 0 {
		t.Fatal("a fresh lazy set must advertise no full schemas")
	}
	if !set.Inject("grep") {
		t.Fatal("expected grep injection to report a change")
	}
	if set.Inject("grep") {
		t.Fatal("re-injecting grep must be a no-op")
	}
	added := set.InjectFromText("now I also want to read_file")
	if !contains(added, "read_file") {
		t.Fatalf("InjectFromText did not add read_file: %v", added)
	}
	if got := set.Specs(); len(got) != 2 {
		t.Fatalf("expected 2 live schemas, got %d", len(got))
	}
}

func TestMeasureSchemaTokens(t *testing.T) {
	r := tools.NewDefaultRegistry()

	none := tools.MeasureSchemaTokens(nil, r, nil)
	if none.Lazy >= none.Eager {
		t.Fatalf("deferred catalogue should be cheaper than full schemas: %+v", none)
	}
	if none.Saved() <= 0 || none.SavedRatio() <= 0 {
		t.Fatalf("expected positive savings, got %+v", none)
	}

	withOne := tools.MeasureSchemaTokens(nil, r, []string{"grep"})
	if withOne.Lazy <= none.Lazy {
		t.Fatal("injecting a schema should raise the lazy cost")
	}
	if withOne.Lazy >= withOne.Eager {
		t.Fatal("one injected schema should still be cheaper than all schemas")
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
