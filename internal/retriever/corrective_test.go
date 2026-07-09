package retriever

import (
	"context"
	"testing"

	"github.com/user/go-rag/internal/storage"
)

type mockQAEvaluator struct {
	quality RetrievalQuality
}

func (m *mockQAEvaluator) Evaluate(_ context.Context, _ string, _ []storage.SearchResult) (RetrievalQuality, error) {
	return m.quality, nil
}

type mockWebSearcher struct {
	results []storage.SearchResult
	calls   int
}

func (m *mockWebSearcher) Search(_ context.Context, _ string, _ int) ([]storage.SearchResult, error) {
	m.calls++
	return m.results, nil
}

type sequenceRewriter struct {
	rewritesByCall [][]string
	calls          int
}

func (m *sequenceRewriter) Rewrite(_ context.Context, _ string, _ int) ([]string, error) {
	m.calls++
	idx := m.calls - 1
	if idx >= len(m.rewritesByCall) {
		return nil, nil
	}
	return m.rewritesByCall[idx], nil
}

func TestHeuristicQAEvaluator_ClassifiesQuality(t *testing.T) {
	e := NewHeuristicQAEvaluator()

	high, _ := e.Evaluate(context.Background(), "q", []storage.SearchResult{
		{Score: 0.95},
		{Score: 0.80},
	})
	if high != RetrievalQualityHigh {
		t.Fatalf("expected HIGH, got %s", high)
	}

	low, _ := e.Evaluate(context.Background(), "q", []storage.SearchResult{
		{Score: 0.02},
	})
	if low != RetrievalQualityLow {
		t.Fatalf("expected LOW, got %s", low)
	}

	uncertain, _ := e.Evaluate(context.Background(), "q", []storage.SearchResult{
		{Score: 0.30},
		{Score: 0.20},
	})
	if uncertain != RetrievalQualityUncertain {
		t.Fatalf("expected UNCERTAIN, got %s", uncertain)
	}
}

func TestRetriever_CorrectiveRAG_HighKeepsRetrieval(t *testing.T) {
	store := &mockStorage{
		chunks: []storage.Chunk{
			{ID: "local-1", DocumentID: "d1", Text: "machine learning basics"},
		},
	}
	ret := NewRetriever(store, nil)
	ret.SetQAEvaluator(&mockQAEvaluator{quality: RetrievalQualityHigh})
	web := &mockWebSearcher{
		results: []storage.SearchResult{
			{Chunk: storage.Chunk{ID: "web-1", DocumentID: "web", Text: "web answer"}, Score: 0.9},
		},
	}
	ret.SetWebSearcher(web)

	results, err := ret.Search(context.Background(), SearchOptions{Query: "machine learning", TopK: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 || results[0].Chunk.ID != "local-1" {
		t.Fatalf("expected local retrieval result, got %+v", results)
	}
	if web.calls != 0 {
		t.Fatalf("expected no web fallback for HIGH, web calls=%d", web.calls)
	}
}

func TestRetriever_CorrectiveRAG_LowUsesWebFallback(t *testing.T) {
	store := &mockStorage{
		chunks: []storage.Chunk{
			{ID: "local-1", DocumentID: "d1", Text: "local"},
		},
	}
	ret := NewRetriever(store, nil)
	ret.SetQAEvaluator(&mockQAEvaluator{quality: RetrievalQualityLow})
	ret.SetWebSearcher(&mockWebSearcher{
		results: []storage.SearchResult{
			{Chunk: storage.Chunk{ID: "web-1", DocumentID: "web", Text: "web fallback answer"}, Score: 0.95},
		},
	})

	results, err := ret.Search(context.Background(), SearchOptions{Query: "unknown", TopK: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 || results[0].Chunk.ID != "web-1" {
		t.Fatalf("expected web fallback result at top, got %+v", results)
	}
}

func TestRetriever_CorrectiveRAG_UncertainExpandsAndRetrievesAgain(t *testing.T) {
	store := &mockStorage{
		chunks: []storage.Chunk{
			{ID: "local-1", DocumentID: "d1", Text: "organelle definition"},
			{ID: "local-2", DocumentID: "d1", Text: "mitochondria function"},
		},
	}
	ret := NewRetriever(store, nil)
	ret.SetQAEvaluator(&mockQAEvaluator{quality: RetrievalQualityUncertain})
	rw := &sequenceRewriter{
		rewritesByCall: [][]string{
			nil,        // first pass: no expansion
			{"mitochondria"}, // corrective pass: expand and re-retrieve
		},
	}
	ret.SetQueryRewriter(rw, 3)

	results, err := ret.Search(context.Background(), SearchOptions{Query: "organelle", TopK: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundExpanded := false
	for _, r := range results {
		if r.Chunk.ID == "local-2" {
			foundExpanded = true
			break
		}
	}
	if !foundExpanded {
		t.Fatalf("expected expanded-query result local-2, got %+v", results)
	}
	if rw.calls < 2 {
		t.Fatalf("expected query rewriter to be called at least twice, got %d", rw.calls)
	}
}
