package retriever

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/user/go-rag/internal/storage"
)

// ---- CrossEncoderReranker tests --------------------------------------------

// mockReranker is a test-only Reranker that applies a fixed score mapping.
type mockReranker struct {
	// scores maps chunk ID to relevance score.
	scores map[string]float64
	err    error
}

func (m *mockReranker) Rerank(_ context.Context, _ string, results []storage.SearchResult) ([]storage.SearchResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make([]storage.SearchResult, len(results))
	for i, r := range results {
		score, ok := m.scores[r.Chunk.ID]
		if !ok {
			score = 0
		}
		out[i] = storage.SearchResult{Chunk: r.Chunk, Score: score}
	}
	// Sort descending by score so callers get a sorted list.
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Score > out[i].Score {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func TestCrossEncoderReranker_Success(t *testing.T) {
	// Spin up a local HTTP server that returns pre-baked relevance scores.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rerankerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Return scores in reverse document order so index 2 > index 1 > index 0.
		resp := rerankerResponse{
			Results: []rerankerResponseItem{
				{Index: 0, RelevanceScore: 0.1},
				{Index: 1, RelevanceScore: 0.5},
				{Index: 2, RelevanceScore: 0.9},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	reranker := NewCrossEncoderReranker(server.URL, "", "test-model")

	results := []storage.SearchResult{
		{Chunk: makeChunk("a", "d", "first"), Score: 0.8},
		{Chunk: makeChunk("b", "d", "second"), Score: 0.6},
		{Chunk: makeChunk("c", "d", "third"), Score: 0.4},
	}

	reranked, err := reranker.Rerank(context.Background(), "query", results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reranked) != 3 {
		t.Fatalf("expected 3 results, got %d", len(reranked))
	}
	// Highest score (0.9) is at index 2 -> chunk "c".
	if reranked[0].Chunk.ID != "c" {
		t.Errorf("expected 'c' as top reranked result, got %q", reranked[0].Chunk.ID)
	}
	// Verify descending sort.
	for i := 1; i < len(reranked); i++ {
		if reranked[i].Score > reranked[i-1].Score {
			t.Errorf("results not sorted descending at index %d", i)
		}
	}
}

func TestCrossEncoderReranker_EmptyResults(t *testing.T) {
	reranker := NewCrossEncoderReranker("http://unused", "", "")
	out, err := reranker.Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("unexpected error for empty input: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got %d items", len(out))
	}
}

func TestCrossEncoderReranker_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	reranker := NewCrossEncoderReranker(server.URL, "", "")
	results := []storage.SearchResult{
		{Chunk: makeChunk("1", "d", "text"), Score: 0.5},
	}
	_, err := reranker.Rerank(context.Background(), "q", results)
	if err == nil {
		t.Fatal("expected an error for HTTP 500, got nil")
	}
}

func TestCrossEncoderReranker_APIKeyHeader(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		resp := rerankerResponse{
			Results: []rerankerResponseItem{{Index: 0, RelevanceScore: 0.8}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reranker := NewCrossEncoderReranker(server.URL, "my-secret-key", "")
	results := []storage.SearchResult{
		{Chunk: makeChunk("1", "d", "text"), Score: 0.5},
	}
	_, err := reranker.Rerank(context.Background(), "q", results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Bearer my-secret-key"
	if receivedAuth != want {
		t.Errorf("expected Authorization header %q, got %q", want, receivedAuth)
	}
}

func TestCrossEncoderReranker_NoAPIKey_NoAuthHeader(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		resp := rerankerResponse{
			Results: []rerankerResponseItem{{Index: 0, RelevanceScore: 0.5}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reranker := NewCrossEncoderReranker(server.URL, "", "")
	results := []storage.SearchResult{
		{Chunk: makeChunk("1", "d", "text"), Score: 0.5},
	}
	_, err := reranker.Rerank(context.Background(), "q", results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuth != "" {
		t.Errorf("expected no Authorization header when apiKey is empty, got %q", receivedAuth)
	}
}

func TestCrossEncoderReranker_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	reranker := NewCrossEncoderReranker(server.URL, "", "")
	results := []storage.SearchResult{
		{Chunk: makeChunk("1", "d", "text"), Score: 0.5},
	}
	_, err := reranker.Rerank(context.Background(), "q", results)
	if err == nil {
		t.Fatal("expected error for invalid JSON response, got nil")
	}
}

// ---- Retriever + Reranker integration tests --------------------------------

func TestRetriever_WithReranker_ReordersResults(t *testing.T) {
	// Set up chunks so that BM25+vector would naturally rank "cooking" higher,
	// but the reranker promotes "machine learning" as the top result.
	vec := []float32{1, 0, 0}
	chunks := []storage.Chunk{
		{ID: "ml", DocumentID: "d1", Text: "machine learning basics", Embedding: []float32{0.5, 0.5, 0}},
		{ID: "dl", DocumentID: "d1", Text: "deep learning networks", Embedding: []float32{1, 0, 0}},
		{ID: "ck", DocumentID: "d1", Text: "cooking recipes", Embedding: []float32{0, 0, 1}},
	}
	store := &mockStorage{chunks: chunks}
	emb := &mockEmbedder{vec: vec}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)

	// Attach a reranker that assigns a very high score to the "ml" chunk.
	rr := &mockReranker{
		scores: map[string]float64{
			"ml": 0.95,
			"dl": 0.40,
			"ck": 0.10,
		},
	}
	ret.SetReranker(rr)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "machine learning",
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	if results[0].Chunk.ID != "ml" {
		t.Errorf("expected 'ml' as top result after reranking, got %q", results[0].Chunk.ID)
	}
}

func TestRetriever_WithReranker_TopKTrimmed(t *testing.T) {
	vec := []float32{1, 0}
	chunks := []storage.Chunk{
		{ID: "1", DocumentID: "d", Text: "alpha", Embedding: []float32{1, 0}},
		{ID: "2", DocumentID: "d", Text: "beta", Embedding: []float32{0.9, 0.1}},
		{ID: "3", DocumentID: "d", Text: "gamma", Embedding: []float32{0.8, 0.2}},
		{ID: "4", DocumentID: "d", Text: "delta", Embedding: []float32{0.7, 0.3}},
		{ID: "5", DocumentID: "d", Text: "epsilon", Embedding: []float32{0.6, 0.4}},
	}
	store := &mockStorage{chunks: chunks}
	emb := &mockEmbedder{vec: vec}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)
	ret.SetReranker(&mockReranker{
		scores: map[string]float64{"1": 0.9, "2": 0.8, "3": 0.7, "4": 0.6, "5": 0.5},
	})

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "test",
		TopK:  2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("expected at most 2 results, got %d", len(results))
	}
}

func TestRetriever_RerankerFallback_OnError(t *testing.T) {
	// When the reranker returns an error, Search should still return results
	// from the original retrieval pipeline (graceful degradation).
	vec := []float32{1, 0}
	chunks := []storage.Chunk{
		{ID: "1", DocumentID: "d", Text: "machine learning", Embedding: []float32{1, 0}},
		{ID: "2", DocumentID: "d", Text: "deep learning", Embedding: []float32{0.9, 0.1}},
	}
	store := &mockStorage{chunks: chunks}
	emb := &mockEmbedder{vec: vec}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)
	ret.SetReranker(&mockReranker{err: errors.New("model unavailable")})

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "learning",
		TopK:  5,
	})
	// Search must not return an error even though the reranker failed.
	if err != nil {
		t.Fatalf("expected graceful fallback, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from fallback, got none")
	}
}

func TestRetriever_NoReranker_OriginalBehaviourUnchanged(t *testing.T) {
	// Ensure that searches without a reranker still work correctly.
	vec := []float32{1, 0}
	chunks := []storage.Chunk{
		{ID: "1", DocumentID: "d", Text: "machine learning", Embedding: []float32{1, 0}},
		{ID: "2", DocumentID: "d", Text: "cooking recipes", Embedding: []float32{0, 1}},
	}
	store := &mockStorage{chunks: chunks}
	emb := &mockEmbedder{vec: vec}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "machine learning",
		TopK:  5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
}
