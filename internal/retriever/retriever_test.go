package retriever

import (
	"context"
	"errors"
	"testing"

	"github.com/liup215/go-rag/internal/storage"
)

// ---- helpers ---------------------------------------------------------------

func makeChunk(id, docID, text string) storage.Chunk {
	return storage.Chunk{ID: id, DocumentID: docID, Text: text}
}

// mockStorage implements the storage.Storage interface for tests.
type mockStorage struct {
	chunks []storage.Chunk
}

func (m *mockStorage) Close() error                                          { return nil }
func (m *mockStorage) CreateDocument(doc *storage.Document) error            { return nil }
func (m *mockStorage) UpdateDocumentStatus(id, status, errMsg string) error  { return nil }
func (m *mockStorage) GetDocument(id string) (*storage.Document, error)      { return nil, nil }
func (m *mockStorage) ListDocuments(limit, offset int) ([]storage.Document, error) {
	return nil, nil
}
func (m *mockStorage) DeleteDocument(id string) error          { return nil }
func (m *mockStorage) CreateChunk(chunk *storage.Chunk) error  { return nil }
func (m *mockStorage) CreateChunks(chunks []storage.Chunk) error { return nil }
func (m *mockStorage) GetChunkByIndex(docID string, index int) (*storage.Chunk, error) {
	return nil, nil
}
func (m *mockStorage) GetChunksByDocument(docID string) ([]storage.Chunk, error) {
	var out []storage.Chunk
	for _, c := range m.chunks {
		if c.DocumentID == docID {
			out = append(out, c)
		}
	}
	return out, nil
}
func (m *mockStorage) GetAllChunks() ([]storage.Chunk, error) {
	return m.chunks, nil
}
func (m *mockStorage) SearchByKeyword(query string, limit int) ([]storage.Chunk, error) {
	return m.chunks, nil
}
func (m *mockStorage) CreateWikiIndex(idx *storage.WikiIndex) error         { return nil }
func (m *mockStorage) GetWikiIndex(id string) (*storage.WikiIndex, error)   { return nil, nil }
func (m *mockStorage) ListWikiIndexes() ([]storage.WikiIndex, error)        { return nil, nil }
func (m *mockStorage) DeleteWikiIndex(id string) error                        { return nil }
func (m *mockStorage) CreateWikiEntry(entry *storage.WikiEntry) error       { return nil }
func (m *mockStorage) GetWikiEntry(id string) (*storage.WikiEntry, error)   { return nil, nil }
func (m *mockStorage) ListWikiEntries(indexID string) ([]storage.WikiEntry, error) {
	return nil, nil
}
func (m *mockStorage) UpdateWikiEntry(entry *storage.WikiEntry) error { return nil }
func (m *mockStorage) DeleteWikiEntry(id string) error                  { return nil }

// mockEmbedder returns a fixed embedding for any input.
type mockEmbedder struct {
	vec []float32
	err error
}

func (e *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = e.vec
	}
	return out, nil
}
func (e *mockEmbedder) Dimension() int    { return len(e.vec) }
func (e *mockEmbedder) ModelName() string { return "mock" }
func (e *mockEmbedder) MaxBatchSize() int { return 10 }

// ---- BM25 tests ------------------------------------------------------------

func TestBuildBM25Index_Empty(t *testing.T) {
	idx := BuildBM25Index(nil)
	results := idx.Search("anything", 5)
	if results != nil {
		t.Errorf("expected nil results for empty index, got %v", results)
	}
}

func TestBM25Index_BasicRanking(t *testing.T) {
	chunks := []storage.Chunk{
		makeChunk("1", "d1", "machine learning is a subset of artificial intelligence"),
		makeChunk("2", "d1", "deep learning neural networks are used in machine learning"),
		makeChunk("3", "d1", "cooking recipes for beginners"),
	}

	idx := BuildBM25Index(chunks)
	results := idx.Search("machine learning", 3)

	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}

	// The top result should be one of the ML chunks, not the cooking chunk.
	if results[0].chunk.ID == "3" {
		t.Errorf("expected an ML chunk as top result, got chunk 3 (cooking)")
	}

	// Scores should be descending.
	for i := 1; i < len(results); i++ {
		if results[i].score > results[i-1].score {
			t.Errorf("results not sorted: score[%d]=%f > score[%d]=%f",
				i, results[i].score, i-1, results[i-1].score)
		}
	}
}

func TestBM25Index_TopKLimit(t *testing.T) {
	chunks := []storage.Chunk{
		makeChunk("1", "d1", "go programming language"),
		makeChunk("2", "d1", "go programming tutorial"),
		makeChunk("3", "d1", "go programming examples"),
		makeChunk("4", "d1", "go programming patterns"),
	}
	idx := BuildBM25Index(chunks)
	results := idx.Search("go programming", 2)
	if len(results) > 2 {
		t.Errorf("expected at most 2 results, got %d", len(results))
	}
}

func TestBM25Index_IDF_Penalises_CommonTerms(t *testing.T) {
	// "the" appears in every chunk; "neural" only in one.
	// When querying "neural network the", the chunk mentioning "neural" should score higher.
	chunks := []storage.Chunk{
		makeChunk("1", "d1", "the cat sat on the mat"),
		makeChunk("2", "d1", "the neural network model is the best"),
		makeChunk("3", "d1", "the dog ran in the park the whole day"),
	}
	idx := BuildBM25Index(chunks)
	results := idx.Search("neural network the", 3)

	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].chunk.ID != "2" {
		t.Errorf("expected chunk 2 (neural) as top result, got %s", results[0].chunk.ID)
	}
}

// ---- RRF tests -------------------------------------------------------------

func TestReciprocalRankFusion_Merges(t *testing.T) {
	vec := []storage.SearchResult{
		{Chunk: makeChunk("a", "d", "text a"), Score: 0.9},
		{Chunk: makeChunk("b", "d", "text b"), Score: 0.8},
	}
	bm := []storage.SearchResult{
		{Chunk: makeChunk("b", "d", "text b"), Score: 5.0},
		{Chunk: makeChunk("c", "d", "text c"), Score: 4.0},
	}

	fused := reciprocalRankFusion(vec, bm)

	// All three unique chunks should be present.
	ids := make(map[string]bool)
	for _, r := range fused {
		ids[r.Chunk.ID] = true
	}
	for _, id := range []string{"a", "b", "c"} {
		if !ids[id] {
			t.Errorf("chunk %q missing from fused results", id)
		}
	}

	// "b" appeared in both lists: rank 1 (index 0) in bm25 and rank 2 (index 1)
	// in vec, giving it the highest combined RRF score.
	if fused[0].Chunk.ID != "b" {
		t.Errorf("expected 'b' as top fused result (appeared in both lists), got %q", fused[0].Chunk.ID)
	}
}

func TestReciprocalRankFusion_EmptyInputs(t *testing.T) {
	if r := reciprocalRankFusion(nil, nil); r != nil && len(r) != 0 {
		t.Errorf("expected empty result for empty inputs, got %v", r)
	}
}

func TestReciprocalRankFusion_Sorted(t *testing.T) {
	vec := []storage.SearchResult{
		{Chunk: makeChunk("a", "d", "text a"), Score: 0.9},
		{Chunk: makeChunk("b", "d", "text b"), Score: 0.7},
		{Chunk: makeChunk("c", "d", "text c"), Score: 0.5},
	}
	bm := []storage.SearchResult{
		{Chunk: makeChunk("c", "d", "text c"), Score: 8.0},
		{Chunk: makeChunk("d", "d", "text d"), Score: 6.0},
	}
	fused := reciprocalRankFusion(vec, bm)
	for i := 1; i < len(fused); i++ {
		if fused[i].Score > fused[i-1].Score {
			t.Errorf("fused results not sorted descending at index %d", i)
		}
	}
}

// ---- Retriever integration tests -------------------------------------------

func TestRetriever_KeywordSearch_NoEmbedder(t *testing.T) {
	store := &mockStorage{
		chunks: []storage.Chunk{
			makeChunk("1", "d1", "machine learning is a subset of artificial intelligence"),
			makeChunk("2", "d1", "deep learning neural networks"),
			makeChunk("3", "d1", "cooking recipes for beginners"),
		},
	}

	ret := NewRetriever(store, nil)
	ret.SetThreshold(0)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "machine learning",
		TopK:  2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	// Top result should be about ML, not cooking.
	if results[0].Chunk.ID == "3" {
		t.Errorf("top result should not be the cooking chunk")
	}
}

func TestRetriever_HybridSearch_WithEmbedder(t *testing.T) {
	vec := []float32{1, 0, 0}
	chunks := []storage.Chunk{
		{ID: "1", DocumentID: "d1", Text: "machine learning", Embedding: []float32{1, 0, 0}},
		{ID: "2", DocumentID: "d1", Text: "deep learning", Embedding: []float32{0.8, 0.2, 0}},
		{ID: "3", DocumentID: "d1", Text: "cooking recipes", Embedding: []float32{0, 0, 1}},
	}

	store := &mockStorage{chunks: chunks}
	emb := &mockEmbedder{vec: vec}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)

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
}

func TestRetriever_HybridSearch_EmbedderError_FallsBackToBM25(t *testing.T) {
	chunks := []storage.Chunk{
		{ID: "1", DocumentID: "d1", Text: "machine learning", Embedding: []float32{1, 0}},
		{ID: "2", DocumentID: "d1", Text: "deep learning", Embedding: []float32{0.9, 0.1}},
	}

	store := &mockStorage{chunks: chunks}
	emb := &mockEmbedder{err: errors.New("api error")}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "learning",
		TopK:  5,
	})
	// Should not return an error; falls back to BM25.
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from BM25 fallback")
	}
}

func TestRetriever_DocumentScopedSearch(t *testing.T) {
	chunks := []storage.Chunk{
		{ID: "1", DocumentID: "docA", Text: "machine learning", Embedding: []float32{1, 0}},
		{ID: "2", DocumentID: "docB", Text: "machine learning advanced", Embedding: []float32{0.9, 0.1}},
	}

	store := &mockStorage{chunks: chunks}
	vec := []float32{1, 0}
	emb := &mockEmbedder{vec: vec}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query:      "machine learning",
		TopK:       5,
		DocumentID: "docA",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		if r.Chunk.DocumentID != "docA" {
			t.Errorf("expected results only from docA, got %s", r.Chunk.DocumentID)
		}
	}
}

func TestRetriever_EmptyStore(t *testing.T) {
	store := &mockStorage{}
	emb := &mockEmbedder{vec: []float32{1, 0}}
	ret := NewRetriever(store, emb)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "anything",
		TopK:  5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results for empty store, got %d", len(results))
	}
}

// ---- tokenize helper tests -------------------------------------------------

func TestTokenize(t *testing.T) {
	cases := []struct {
		input    string
		wantLen  int
		wantFirst string
	}{
		{"Hello World", 2, "hello"},
		{"machine-learning", 2, "machine"},
		{"  spaces  ", 1, "spaces"},
		{"", 0, ""},
	}

	for _, tc := range cases {
		tokens := tokenize(tc.input)
		if len(tokens) != tc.wantLen {
			t.Errorf("tokenize(%q) length = %d, want %d", tc.input, len(tokens), tc.wantLen)
		}
		if tc.wantLen > 0 && tokens[0] != tc.wantFirst {
			t.Errorf("tokenize(%q) first token = %q, want %q", tc.input, tokens[0], tc.wantFirst)
		}
	}
}

func TestCosineSimilarity(t *testing.T) {
	// Identical vectors → similarity 1.
	a := []float32{1, 0, 0}
	if s := cosineSimilarity(a, a); s < 0.999 {
		t.Errorf("expected ~1 for identical vectors, got %f", s)
	}

	// Orthogonal vectors → similarity 0.
	b := []float32{0, 1, 0}
	if s := cosineSimilarity(a, b); s > 0.001 {
		t.Errorf("expected ~0 for orthogonal vectors, got %f", s)
	}

	// Mismatched lengths → 0.
	c := []float32{1, 0}
	if s := cosineSimilarity(a, c); s != 0 {
		t.Errorf("expected 0 for mismatched lengths, got %f", s)
	}
}
