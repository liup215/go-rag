package retriever

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/user/go-rag/internal/storage"
)

// RetrievalQuality indicates retrieval quality judged by a QA evaluator.
type RetrievalQuality string

const (
	RetrievalQualityHigh      RetrievalQuality = "HIGH"
	RetrievalQualityLow       RetrievalQuality = "LOW"
	RetrievalQualityUncertain RetrievalQuality = "UNCERTAIN"
)

// QAEvaluator evaluates retrieval quality for Corrective RAG.
type QAEvaluator interface {
	Evaluate(ctx context.Context, query string, results []storage.SearchResult) (RetrievalQuality, error)
}

// WebSearcher performs external fallback search when local retrieval quality is low.
type WebSearcher interface {
	Search(ctx context.Context, query string, topK int) ([]storage.SearchResult, error)
}

// HeuristicQAEvaluator evaluates quality using simple score/coverage heuristics.
type HeuristicQAEvaluator struct {
	HighScoreThreshold float64
	LowScoreThreshold  float64
	MinHighResults     int
}

// NewHeuristicQAEvaluator creates a default heuristic evaluator.
func NewHeuristicQAEvaluator() *HeuristicQAEvaluator {
	return &HeuristicQAEvaluator{
		HighScoreThreshold: 0.60,
		LowScoreThreshold:  0.10,
		MinHighResults:     2,
	}
}

// Evaluate classifies retrieval quality into HIGH/LOW/UNCERTAIN.
func (e *HeuristicQAEvaluator) Evaluate(
	_ context.Context,
	_ string,
	results []storage.SearchResult,
) (RetrievalQuality, error) {
	if len(results) == 0 {
		return RetrievalQualityLow, nil
	}

	top := results[0].Score
	if top >= e.HighScoreThreshold && len(results) >= e.MinHighResults {
		return RetrievalQualityHigh, nil
	}
	if top <= e.LowScoreThreshold {
		return RetrievalQualityLow, nil
	}
	return RetrievalQualityUncertain, nil
}

// LLMQAEvaluator evaluates retrieval quality with an HTTP LLM endpoint.
type LLMQAEvaluator struct {
	url    string
	apiKey string
	model  string
	client *http.Client
}

type qaRequest struct {
	Model   string   `json:"model,omitempty"`
	Query   string   `json:"query"`
	Results []string `json:"results"`
}

type qaResponse struct {
	Quality string `json:"quality"`
}

// NewLLMQAEvaluator creates an LLM-backed QA evaluator.
func NewLLMQAEvaluator(url, apiKey, model string) *LLMQAEvaluator {
	return &LLMQAEvaluator{
		url:    url,
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// Evaluate calls the LLM evaluator and returns HIGH/LOW/UNCERTAIN.
func (e *LLMQAEvaluator) Evaluate(
	ctx context.Context,
	query string,
	results []storage.SearchResult,
) (RetrievalQuality, error) {
	if e.url == "" {
		return RetrievalQualityUncertain, fmt.Errorf("qa evaluator URL is empty")
	}

	texts := make([]string, 0, len(results))
	for _, res := range results {
		texts = append(texts, res.Chunk.Text)
	}

	body, err := json.Marshal(qaRequest{
		Model:   e.model,
		Query:   query,
		Results: texts,
	})
	if err != nil {
		return RetrievalQualityUncertain, fmt.Errorf("qa evaluator: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return RetrievalQualityUncertain, fmt.Errorf("qa evaluator: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return RetrievalQualityUncertain, fmt.Errorf("qa evaluator: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return RetrievalQualityUncertain, fmt.Errorf("qa evaluator: HTTP %d: %s", resp.StatusCode, snippet)
	}

	var out qaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RetrievalQualityUncertain, fmt.Errorf("qa evaluator: decode response: %w", err)
	}

	switch RetrievalQuality(strings.ToUpper(strings.TrimSpace(out.Quality))) {
	case RetrievalQualityHigh:
		return RetrievalQualityHigh, nil
	case RetrievalQualityLow:
		return RetrievalQualityLow, nil
	case RetrievalQualityUncertain:
		return RetrievalQualityUncertain, nil
	default:
		return RetrievalQualityUncertain, nil
	}
}

