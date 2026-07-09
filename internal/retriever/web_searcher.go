package retriever

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/user/go-rag/internal/storage"
)

// HTTPWebSearcher calls an external web search endpoint for fallback retrieval.
type HTTPWebSearcher struct {
	url    string
	apiKey string
	client *http.Client
}

type webSearchRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

type webSearchItem struct {
	Title   string  `json:"title"`
	Content string  `json:"content"`
	URL     string  `json:"url"`
	Score   float64 `json:"score"`
}

type webSearchResponse struct {
	Results []webSearchItem `json:"results"`
}

// NewHTTPWebSearcher creates a new HTTP web fallback searcher.
func NewHTTPWebSearcher(url, apiKey string) *HTTPWebSearcher {
	return &HTTPWebSearcher{
		url:    url,
		apiKey: apiKey,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// Search executes a web search and converts results to SearchResult.
func (s *HTTPWebSearcher) Search(
	ctx context.Context,
	query string,
	topK int,
) ([]storage.SearchResult, error) {
	if s.url == "" {
		return nil, fmt.Errorf("web search URL is empty")
	}
	if topK <= 0 {
		topK = 5
	}

	body, err := json.Marshal(webSearchRequest{Query: query, TopK: topK})
	if err != nil {
		return nil, fmt.Errorf("web search: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("web search: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web search: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("web search: HTTP %d: %s", resp.StatusCode, snippet)
	}

	var out webSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("web search: decode response: %w", err)
	}

	results := make([]storage.SearchResult, 0, len(out.Results))
	for i, item := range out.Results {
		chunkID := fmt.Sprintf("web-%d", i)
		text := item.Content
		if text == "" {
			text = item.Title
		}
		results = append(results, storage.SearchResult{
			Chunk: storage.Chunk{
				ID:         chunkID,
				DocumentID: item.URL,
				Text:       text,
				Index:      i,
			},
			Score: item.Score,
		})
	}
	return results, nil
}

