package retriever

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/user/go-rag/internal/embedder"
	"github.com/user/go-rag/internal/storage"
)

// rrfK is the constant used in Reciprocal Rank Fusion (RRF).
// A value of 60 is recommended in the original RRF paper.
const rrfK = 60

// candidateMultiplier controls how many candidates each retriever fetches
// before RRF fusion.  Fetching more candidates improves recall at the cost of
// latency.  The final result set is always trimmed to TopK.
const candidateMultiplier = 10

// Retriever performs hybrid search on the knowledge base.
type Retriever struct {
	storage   storage.Storage
	embedder  embedder.Embedder
	threshold float64
}

// NewRetriever creates a new Retriever.
func NewRetriever(store storage.Storage, emb embedder.Embedder) *Retriever {
	return &Retriever{
		storage:   store,
		embedder:  emb,
		threshold: 0.5, // Default similarity threshold
	}
}

// SetThreshold sets the minimum similarity score for results.
func (r *Retriever) SetThreshold(threshold float64) {
	r.threshold = threshold
}

// SearchOptions contains search parameters.
type SearchOptions struct {
	Query      string
	TopK       int
	Threshold  float64
	DocumentID string
}

// Search performs hybrid search (vector + BM25) when an embedder is available,
// fusing the results with Reciprocal Rank Fusion.  When no embedder is
// configured it falls back to BM25-only keyword search.
func (r *Retriever) Search(ctx context.Context, opts SearchOptions) ([]storage.SearchResult, error) {
	if opts.TopK <= 0 {
		opts.TopK = 5
	}
	if opts.Threshold == 0 {
		opts.Threshold = r.threshold
	}

	if r.embedder != nil {
		return r.hybridSearch(ctx, opts)
	}

	return r.keywordSearch(opts)
}

// hybridSearch runs vector similarity search and BM25 in parallel over the
// same candidate set, then fuses the ranked lists with RRF.
func (r *Retriever) hybridSearch(ctx context.Context, opts SearchOptions) ([]storage.SearchResult, error) {
	// Load all embedded chunks once; they are used for both retrieval methods.
	chunks, err := r.storage.GetAllChunks()
	if err != nil {
		return nil, fmt.Errorf("failed to get chunks: %w", err)
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	// Apply optional document-scoped filter.
	if opts.DocumentID != "" {
		filtered := chunks[:0]
		for _, c := range chunks {
			if c.DocumentID == opts.DocumentID {
				filtered = append(filtered, c)
			}
		}
		chunks = filtered
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	// Each retriever produces a wider candidate pool for better recall before fusion.
	candidateK := opts.TopK * candidateMultiplier
	if candidateK < 20 {
		candidateK = 20
	}

	// --- Vector search ---
	vecResults, vecErr := r.vectorSearchOnChunks(ctx, chunks, opts, candidateK)
	if vecErr != nil {
		// If embedding fails fall back to BM25-only.
		idx := BuildBM25Index(chunks)
		bm25Raw := idx.Search(opts.Query, opts.TopK)
		results := make([]storage.SearchResult, len(bm25Raw))
		for i, br := range bm25Raw {
			results[i] = storage.SearchResult{Chunk: br.chunk, Score: br.score}
		}
		return results, nil
	}

	// --- BM25 search ---
	idx := BuildBM25Index(chunks)
	bm25Raw := idx.Search(opts.Query, candidateK)
	bm25Results := make([]storage.SearchResult, len(bm25Raw))
	for i, br := range bm25Raw {
		bm25Results[i] = storage.SearchResult{Chunk: br.chunk, Score: br.score}
	}

	// --- RRF fusion ---
	fused := reciprocalRankFusion(vecResults, bm25Results)
	if len(fused) > opts.TopK {
		fused = fused[:opts.TopK]
	}

	return fused, nil
}

// vectorSearchOnChunks scores a pre-loaded slice of chunks against the query
// embedding and returns up to candidateK results above the similarity threshold.
func (r *Retriever) vectorSearchOnChunks(
	ctx context.Context,
	chunks []storage.Chunk,
	opts SearchOptions,
	candidateK int,
) ([]storage.SearchResult, error) {
	queryEmbeddings, err := r.embedder.Embed(ctx, []string{opts.Query})
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}
	if len(queryEmbeddings) == 0 || len(queryEmbeddings[0]) == 0 {
		return nil, fmt.Errorf("empty query embedding")
	}
	queryVec := queryEmbeddings[0]

	type sv struct {
		chunk storage.Chunk
		score float64
	}
	scored := make([]sv, 0, len(chunks))

	for _, chunk := range chunks {
		if len(chunk.Embedding) == 0 || len(chunk.Embedding) != len(queryVec) {
			continue
		}
		score := cosineSimilarity(queryVec, chunk.Embedding)
		if score >= opts.Threshold {
			scored = append(scored, sv{chunk, score})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	if len(scored) > candidateK {
		scored = scored[:candidateK]
	}

	results := make([]storage.SearchResult, len(scored))
	for i, s := range scored {
		results[i] = storage.SearchResult{Chunk: s.chunk, Score: s.score}
	}
	return results, nil
}

// keywordSearch performs BM25-based keyword search without a vector embedder.
func (r *Retriever) keywordSearch(opts SearchOptions) ([]storage.SearchResult, error) {
	var chunks []storage.Chunk
	var err error

	if opts.DocumentID != "" {
		// Score all chunks of the target document.
		chunks, err = r.storage.GetChunksByDocument(opts.DocumentID)
	} else {
		// Pre-filter with a keyword LIKE query to reduce the candidate set,
		// then apply BM25 scoring on top.
		limit := opts.TopK * 10
		if limit < 50 {
			limit = 50
		}
		chunks, err = r.storage.SearchByKeyword(opts.Query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("keyword search failed: %w", err)
	}

	idx := BuildBM25Index(chunks)
	bm25Raw := idx.Search(opts.Query, opts.TopK)

	results := make([]storage.SearchResult, len(bm25Raw))
	for i, br := range bm25Raw {
		results[i] = storage.SearchResult{Chunk: br.chunk, Score: br.score}
	}
	return results, nil
}

// reciprocalRankFusion fuses two ranked result lists using Reciprocal Rank
// Fusion (RRF).  For each list, the item at position i (0-based) receives a
// score contribution of 1/(rrfK + i + 1), which matches the standard RRF
// formula with 1-based rank indexing.  Contributions from all lists are summed
// and the merged list is returned sorted by descending combined score.
func reciprocalRankFusion(vecResults, bm25Results []storage.SearchResult) []storage.SearchResult {
	scores := make(map[string]float64)
	byID := make(map[string]storage.Chunk)

	for rank, res := range vecResults {
		scores[res.Chunk.ID] += 1.0 / float64(rrfK+rank+1)
		byID[res.Chunk.ID] = res.Chunk
	}
	for rank, res := range bm25Results {
		scores[res.Chunk.ID] += 1.0 / float64(rrfK+rank+1)
		if _, exists := byID[res.Chunk.ID]; !exists {
			byID[res.Chunk.ID] = res.Chunk
		}
	}

	results := make([]storage.SearchResult, 0, len(scores))
	for id, score := range scores {
		results = append(results, storage.SearchResult{
			Chunk: byID[id],
			Score: score,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// cosineSimilarity calculates cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}

	if na == 0 || nb == 0 {
		return 0
	}

	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// tokenize splits text into normalised tokens.
func tokenize(s string) []string {
	words := splitWords(s)
	for i := range words {
		words[i] = normalizeWord(words[i])
	}
	return filterEmpty(words)
}

// Helper functions

func splitWords(s string) []string {
	var words []string
	var current []rune

	for _, r := range s {
		if isWordChar(r) {
			current = append(current, r)
		} else if len(current) > 0 {
			words = append(words, string(current))
			current = nil
		}
	}

	if len(current) > 0 {
		words = append(words, string(current))
	}

	return words
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r > 127 // Non-ASCII (e.g., CJK)
}

func normalizeWord(s string) string {
	// Convert to lowercase for ASCII
	var result []rune
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			result = append(result, r+('a'-'A'))
		} else {
			result = append(result, r)
		}
	}
	return string(result)
}

func filterEmpty(ss []string) []string {
	var result []string
	for _, s := range ss {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}
