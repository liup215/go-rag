package retriever

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/liup215/go-rag/internal/storage"
)

// Reranker reorders a list of search results by relevance to the query.
// Implementations may call an external cross-encoder model API or apply a
// local scoring heuristic.
type Reranker interface {
	// Rerank scores each result against the query and returns results sorted
	// by descending relevance score.  The returned slice may be shorter than
	// the input if the implementation applies an internal threshold.
	Rerank(ctx context.Context, query string, results []storage.SearchResult) ([]storage.SearchResult, error)
}

// rerankerCandidateMultiplier controls how many candidates are fetched during
// the initial retrieval step before handing them to the reranker.  A larger
// value improves recall at the cost of extra reranking latency.
const rerankerCandidateMultiplier = 3

// CrossEncoderReranker calls an HTTP cross-encoder API (e.g. a locally served
// bge-reranker, Jina Reranker, or Cohere Rerank) to score query–document pairs.
//
// Expected request body (JSON):
//
//	{"model":"<model>","query":"<q>","documents":["<d1>","<d2>",...]}
//
// Expected response body (JSON):
//
//	{"results":[{"index":0,"relevance_score":0.95}, ...]}
//
// The "model" field is omitted when the model name is empty, letting the server
// use its default model.
type CrossEncoderReranker struct {
	url    string
	apiKey string
	model  string
	client *http.Client
}

// rerankerRequest is the JSON payload sent to the cross-encoder API.
type rerankerRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

// rerankerResponseItem holds the score returned for one document.
type rerankerResponseItem struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// rerankerResponse is the top-level JSON body returned by the cross-encoder API.
type rerankerResponse struct {
	Results []rerankerResponseItem `json:"results"`
}

// NewCrossEncoderReranker creates a CrossEncoderReranker that POSTs to the
// given URL.  apiKey is sent as a Bearer token when non-empty.  model is
// included in the request body; it may be empty if the server selects a
// default model.
func NewCrossEncoderReranker(url, apiKey, model string) *CrossEncoderReranker {
	return &CrossEncoderReranker{
		url:    url,
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Rerank calls the cross-encoder API, applies the returned relevance scores,
// and returns the results sorted by descending score.
func (r *CrossEncoderReranker) Rerank(
	ctx context.Context,
	query string,
	results []storage.SearchResult,
) ([]storage.SearchResult, error) {
	if len(results) == 0 {
		return results, nil
	}

	// Collect document texts to send for scoring.
	docs := make([]string, len(results))
	for i, res := range results {
		docs[i] = res.Chunk.Text
	}

	payload := rerankerRequest{
		Model:     r.model,
		Query:     query,
		Documents: docs,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("reranker: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("reranker: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reranker: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("reranker: HTTP %d: %s", resp.StatusCode, snippet)
	}

	var rerankResp rerankerResponse
	if err := json.NewDecoder(resp.Body).Decode(&rerankResp); err != nil {
		return nil, fmt.Errorf("reranker: decode response: %w", err)
	}

	// Map returned scores back to the original results and sort by relevance.
	reranked := make([]storage.SearchResult, 0, len(rerankResp.Results))
	for _, item := range rerankResp.Results {
		if item.Index < 0 || item.Index >= len(results) {
			continue
		}
		res := results[item.Index]
		res.Score = item.RelevanceScore
		reranked = append(reranked, res)
	}

	sort.Slice(reranked, func(i, j int) bool {
		return reranked[i].Score > reranked[j].Score
	})

	return reranked, nil
}
