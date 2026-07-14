package retriever

import (
	"math"
	"sort"

	"github.com/liup215/go-rag/internal/storage"
)

const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

// BM25Index is an in-memory BM25 index over a set of chunks.
type BM25Index struct {
	chunks []storage.Chunk
	tf     []map[string]float64 // tf[i][term] = frequency of term in chunk i
	df     map[string]int       // df[term] = number of chunks containing term
	avgDL  float64              // average document length in tokens
}

// bm25Result holds a chunk and its BM25 score.
type bm25Result struct {
	chunk storage.Chunk
	score float64
}

// BuildBM25Index builds a BM25 index from a slice of chunks.
func BuildBM25Index(chunks []storage.Chunk) *BM25Index {
	idx := &BM25Index{
		chunks: chunks,
		tf:     make([]map[string]float64, len(chunks)),
		df:     make(map[string]int),
	}

	totalLen := 0
	for i, chunk := range chunks {
		tokens := tokenize(chunk.Text)
		freq := make(map[string]float64, len(tokens))
		for _, t := range tokens {
			freq[t]++
		}
		idx.tf[i] = freq
		totalLen += len(tokens)

		// Count each term once per chunk for DF.
		for term := range freq {
			idx.df[term]++
		}
	}

	if len(chunks) > 0 {
		idx.avgDL = float64(totalLen) / float64(len(chunks))
	}

	return idx
}

// Search returns at most topK chunks ranked by BM25 score for the given query.
func (idx *BM25Index) Search(query string, topK int) []bm25Result {
	if len(idx.chunks) == 0 || topK <= 0 || idx.avgDL == 0 {
		return nil
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	n := float64(len(idx.chunks))
	results := make([]bm25Result, 0, len(idx.chunks))

	for i, chunk := range idx.chunks {
		score := idx.scoreDoc(i, queryTokens, n)
		if score > 0 {
			results = append(results, bm25Result{chunk: chunk, score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results
}

// scoreDoc computes the BM25 score for the chunk at docIdx given query tokens.
func (idx *BM25Index) scoreDoc(docIdx int, queryTokens []string, n float64) float64 {
	docFreqs := idx.tf[docIdx]

	// Compute document length (total term count).
	dl := float64(0)
	for _, f := range docFreqs {
		dl += f
	}

	var score float64
	for _, term := range queryTokens {
		tf := docFreqs[term]
		if tf == 0 {
			continue
		}

		df := float64(idx.df[term])
		// Robertson-Sparck Jones IDF with smoothing.
		idf := math.Log((n-df+0.5)/(df+0.5) + 1)

		// BM25 term frequency component.
		norm := bm25K1 * (1 - bm25B + bm25B*dl/idx.avgDL)
		score += idf * (tf * (bm25K1 + 1)) / (tf + norm)
	}

	return score
}
