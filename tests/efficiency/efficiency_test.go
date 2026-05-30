package efficiency_test

import (
	"testing"

	"github.com/apex-code/apex/internal/tools"
)

func TestLazyToolSchemasStayMeaningfullyCheaper(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	tokens := tools.MeasureSchemaTokens(nil, registry, []string{"read_file"})
	if tokens.Saved() <= 0 {
		t.Fatalf("expected lazy schemas to save tokens, got %+v", tokens)
	}
	if tokens.SavedRatio() < 0.35 {
		t.Fatalf("lazy schema savings regressed: got %.2f want >= 0.35", tokens.SavedRatio())
	}
}
