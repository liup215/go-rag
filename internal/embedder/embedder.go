package embedder

import (
	"context"
)

// Embedder produces dense vector embeddings for text.
type Embedder interface {
	// Embed generates embeddings for multiple texts.
	// Returns a slice of float32 vectors, one per input text.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Dimension returns the dimension of the embedding vectors.
	Dimension() int

	// ModelName returns the name of the model being used.
	ModelName() string

	// MaxBatchSize returns the maximum number of texts per batch.
	MaxBatchSize() int
}

// NormalizeL2 normalizes a vector to unit length (L2 norm).
// This makes cosine similarity equivalent to dot product.
func NormalizeL2(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}

	if sum == 0 {
		return v
	}

	norm := float32(sum)
	for i := range v {
		v[i] /= norm
	}

	return v
}

// CosineSimilarity calculates cosine similarity between two vectors.
// Assumes vectors are already normalized for efficiency.
func CosineSimilarity(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, nil
	}

	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}

	return dot, nil
}

// CosineSimilarityRaw calculates cosine similarity without normalization assumption.
func CosineSimilarityRaw(a, b []float32) float64 {
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

	return dot / (na * nb)
}

// TopK finds the top k most similar vectors.
// Returns indices and similarity scores.
func TopK(query []float32, vectors [][]float32, k int) ([]int, []float64) {
	if k <= 0 || len(vectors) == 0 {
		return nil, nil
	}

	// Score all vectors
	type scored struct {
		idx int
		sim float64
	}

	scores := make([]scored, len(vectors))
	for i, v := range vectors {
		sim, _ := CosineSimilarity(query, v)
		scores[i] = scored{i, sim}
	}

	// Simple bubble sort for small N
	for i := 0; i < len(scores)-1; i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[j].sim > scores[i].sim {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}

	// Return top k
	if k > len(scores) {
		k = len(scores)
	}

	indices := make([]int, k)
	sims := make([]float64, k)
	for i := 0; i < k; i++ {
		indices[i] = scores[i].idx
		sims[i] = scores[i].sim
	}

	return indices, sims
}
