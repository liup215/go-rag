package retriever

import (
	"context"
	"testing"

	"github.com/user/go-rag/internal/storage"
)

type fixedQueryRewriter struct {
	queries []string
}

func (f *fixedQueryRewriter) Rewrite(_ context.Context, _ string, _ int) ([]string, error) {
	return f.queries, nil
}

func TestRuleBasedQueryRewriter_TermExtraction(t *testing.T) {
	rw := NewRuleBasedQueryRewriter()
	queries, err := rw.Rewrite(context.Background(), "细胞的能量工厂是什么", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, q := range queries {
		if q == "细胞的线粒体是什么" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected term extraction rewrite, got %v", queries)
	}
}

func TestRuleBasedQueryRewriter_SynonymExpansion(t *testing.T) {
	rw := NewRuleBasedQueryRewriter()
	queries, err := rw.Rewrite(context.Background(), "光合作用的过程", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, q := range queries {
		if q == "光合作用的过程 photosynthesis" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected synonym expansion rewrite, got %v", queries)
	}
}

func TestRuleBasedQueryRewriter_HyponymExpansion(t *testing.T) {
	rw := NewRuleBasedQueryRewriter()
	queries, err := rw.Rewrite(context.Background(), "细胞器的功能", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) < 2 {
		t.Fatalf("expected at least two hyponym rewrites, got %v", queries)
	}
}

func TestRuleBasedQueryRewriter_RespectsMaxQueries(t *testing.T) {
	rw := NewRuleBasedQueryRewriter()
	queries, err := rw.Rewrite(context.Background(), "细胞器和光合作用还有能量工厂", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("expected exactly 2 rewrites, got %d (%v)", len(queries), queries)
	}
}

func TestRetriever_MultiQueryExpansion_DeduplicatesMergedResults(t *testing.T) {
	store := &mockStorage{
		chunks: []storage.Chunk{
			{ID: "1", DocumentID: "d1", Text: "线粒体是细胞器"},
			{ID: "1", DocumentID: "d1", Text: "线粒体是细胞器"}, // duplicate ID
			{ID: "2", DocumentID: "d1", Text: "叶绿体参与光合作用"},
		},
	}
	ret := NewRetriever(store, nil)
	ret.SetQueryRewriter(&fixedQueryRewriter{
		queries: []string{"线粒体", "叶绿体", "线粒体"},
	}, 5)

	results, err := ret.Search(context.Background(), SearchOptions{Query: "细胞器", TopK: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	seen := make(map[string]bool)
	for _, r := range results {
		if seen[r.Chunk.ID] {
			t.Fatalf("expected deduplicated results, duplicate ID found: %s", r.Chunk.ID)
		}
		seen[r.Chunk.ID] = true
	}
}
