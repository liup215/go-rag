package retriever

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/user/go-rag/internal/embedder"
	"github.com/user/go-rag/internal/storage"
)

// Retriever performs hybrid search on the knowledge base.
type Retriever struct {
	storage   *storage.Storage
	embedder  embedder.Embedder
	threshold float64
}

// NewRetriever creates a new Retriever.
func NewRetriever(storage *storage.Storage, embedder embedder.Embedder) *Retriever {
	return &Retriever{
		storage:   storage,
		embedder:  embedder,
		threshold: 0.5, // Default similarity threshold
	}
}

// SetThreshold sets the minimum similarity score for results.
func (r *Retriever) SetThreshold(threshold float64) {
	r.threshold = threshold
}

// SearchOptions contains search parameters.
type SearchOptions struct {
	Query     string
	TopK      int
	Threshold float64
}

// Search performs hybrid search (vector + keyword fallback).
func (r *Retriever) Search(ctx context.Context, opts SearchOptions) ([]storage.SearchResult, error) {
	if opts.TopK <= 0 {
		opts.TopK = 5
	}
	if opts.Threshold == 0 {
		opts.Threshold = r.threshold
	}

	// Try vector search first if embedder is available
	if r.embedder != nil {
		results, err := r.vectorSearch(ctx, opts)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		// Fall back to keyword search on error
	}

	// Fallback to keyword search
	return r.keywordSearch(opts)
}

// vectorSearch performs vector-based similarity search.
func (r *Retriever) vectorSearch(ctx context.Context, opts SearchOptions) ([]storage.SearchResult, error) {
	// Get query embedding
	queryEmbeddings, err := r.embedder.Embed(ctx, []string{opts.Query})
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	if len(queryEmbeddings) == 0 || len(queryEmbeddings[0]) == 0 {
		return nil, fmt.Errorf("empty query embedding")
	}

	queryVec := queryEmbeddings[0]

	// Get all chunks with embeddings
	chunks, err := r.storage.GetAllChunks()
	if err != nil {
		return nil, fmt.Errorf("failed to get chunks: %w", err)
	}

	if len(chunks) == 0 {
		return nil, nil
	}

	// Score all chunks
	type scoredChunk struct {
		chunk storage.Chunk
		score float64
	}

	var scored []scoredChunk
	for _, chunk := range chunks {
		if len(chunk.Embedding) == 0 || len(chunk.Embedding) != len(queryVec) {
			continue
		}

		score := cosineSimilarity(queryVec, chunk.Embedding)
		if score >= opts.Threshold {
			scored = append(scored, scoredChunk{chunk: chunk, score: score})
		}
	}

	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Take top K
	if len(scored) > opts.TopK {
		scored = scored[:opts.TopK]
	}

	// Convert to results
	results := make([]storage.SearchResult, len(scored))
	for i, sc := range scored {
		results[i] = storage.SearchResult{
			Chunk: sc.chunk,
			Score: sc.score,
		}
	}

	return results, nil
}

// keywordSearch performs keyword-based search using FTS5.
func (r *Retriever) keywordSearch(opts SearchOptions) ([]storage.SearchResult, error) {
	// Get more results than needed for better coverage
	limit := opts.TopK * 3
	if limit < 20 {
		limit = 20
	}

	chunks, err := r.storage.SearchByKeyword(opts.Query, limit)
	if err != nil {
		return nil, fmt.Errorf("keyword search failed: %w", err)
	}

	// Score results based on keyword overlap
	queryTokens := tokenize(opts.Query)

	type scoredChunk struct {
		chunk storage.Chunk
		score float64
	}

	var scored []scoredChunk
	for _, chunk := range chunks {
		score := keywordScore(chunk.Text, queryTokens)
		if score >= opts.Threshold {
			scored = append(scored, scoredChunk{chunk: chunk, score: score})
		}
	}

	// Sort by score
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Take top K
	if len(scored) > opts.TopK {
		scored = scored[:opts.TopK]
	}

	results := make([]storage.SearchResult, len(scored))
	for i, sc := range scored {
		results[i] = storage.SearchResult{
			Chunk: sc.chunk,
			Score: sc.score,
		}
	}

	return results, nil
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

// tokenize splits text into tokens.
func tokenize(s string) []string {
	words := splitWords(s)
	for i := range words {
		words[i] = normalizeWord(words[i])
	}
	return filterEmpty(words)
}

// keywordScore calculates keyword overlap score.
func keywordScore(text string, queryTokens []string) float64 {
	textTokens := tokenize(text)
	if len(textTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}

	// Build set of text tokens
	textSet := make(map[string]bool)
	for _, t := range textTokens {
		textSet[t] = true
	}

	// Count matches
	matches := 0
	for _, q := range queryTokens {
		if textSet[q] {
			matches++
		}
	}

	// Return normalized score
	return float64(matches) / float64(len(queryTokens))
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
