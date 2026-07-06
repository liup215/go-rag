package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// OpenAIEmbedder calls any OpenAI-compatible embedding API.
type OpenAIEmbedder struct {
	APIKey           string
	BaseURL          string
	Model            string
	Dim              int
	MaxBatch         int
	InputTokenBudget int
	HTTPClient       *http.Client
}

// NewOpenAIEmbedder creates an embedder for OpenAI-compatible APIs.
func NewOpenAIEmbedder(apiKey, baseURL, model string) *OpenAIEmbedder {
	if model == "" {
		model = "text-embedding-3-small"
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	return &OpenAIEmbedder{
		APIKey:           apiKey,
		BaseURL:          baseURL,
		Model:            model,
		Dim:              1536, // text-embedding-3-small
		MaxBatch:         100,
		InputTokenBudget: 30000,
		HTTPClient:       &http.Client{Timeout: 120 * time.Second},
	}
}

// Dimension returns the embedding dimension.
func (e *OpenAIEmbedder) Dimension() int { return e.Dim }

// ModelName returns the model name.
func (e *OpenAIEmbedder) ModelName() string { return e.Model }

// MaxBatchSize returns the maximum batch size.
func (e *OpenAIEmbedder) MaxBatchSize() int { return e.MaxBatch }

// Embed generates embeddings for the given texts.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var all [][]float32
	start := 0

	for start < len(texts) {
		end := e.nextBatchEnd(texts, start)
		batch, err := e.embedBatchWithRetry(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", start, end, err)
		}
		all = append(all, batch...)
		start = end
	}

	return all, nil
}

// nextBatchEnd returns the largest end index for a batch.
func (e *OpenAIEmbedder) nextBatchEnd(texts []string, start int) int {
	n := len(texts)
	end := start + 1
	if end > n {
		end = n
	}

	tokens := 0
	for end <= n && end-start <= e.MaxBatch {
		tokens += estimateTokensLocal(texts[end-1])
		if tokens > e.InputTokenBudget {
			if end-1 == start {
				return end
			}
			return end - 1
		}
		end++
	}

	return end - 1
}

// embedBatchWithRetry attempts to embed a batch with retries.
func (e *OpenAIEmbedder) embedBatchWithRetry(ctx context.Context, texts []string) ([][]float32, error) {
	const maxAttempts = 3
	backoff := time.Second
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		vecs, err := e.embedBatch(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		lastErr = err

		if !isRetryable(err) || attempt == maxAttempts {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	return nil, lastErr
}

// embedBatch sends a single batch to the API.
func (e *OpenAIEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	payload := map[string]interface{}{
		"model": e.Model,
		"input": texts,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := e.BaseURL
	// If URL already ends with /embeddings, use it as-is (user provided full path)
	if hasSuffix(url, "/embeddings") {
		// Use as-is
	} else if hasSuffix(url, "/v1") {
		url += "/embeddings"
	} else if contains(url, "/v1/") {
		// URL contains /v1/ somewhere, assume it's a custom path
		url += "/embeddings"
	} else {
		// No /v1 in URL, add standard OpenAI path
		url = strings.TrimSuffix(url, "/") + "/v1/embeddings"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embeddings API %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("embeddings API error: %s", result.Error.Message)
	}

	// Re-order by index and convert to float32
	vecs := make([][]float32, len(texts))
	for _, d := range result.Data {
		v := make([]float32, len(d.Embedding))
		for i := range d.Embedding {
			v[i] = float32(d.Embedding[i])
		}
		vecs[d.Index] = v
	}

	// Normalize vectors
	for i := range vecs {
		vecs[i] = NormalizeL2(vecs[i])
	}

	return vecs, nil
}

// OllamaEmbedder calls a local Ollama server for embeddings.
type OllamaEmbedder struct {
	BaseURL          string
	Model            string
	Dim              int
	MaxBatch         int
	InputTokenBudget int
	HTTPClient       *http.Client
}

// NewOllamaEmbedder creates an embedder for local Ollama.
func NewOllamaEmbedder(model string) *OllamaEmbedder {
	if model == "" {
		model = "nomic-embed-text"
	}

	return &OllamaEmbedder{
		BaseURL:          "http://localhost:11434",
		Model:            model,
		Dim:              768, // nomic-embed-text
		MaxBatch:         1,   // Ollama processes one at a time
		InputTokenBudget: 8192,
		HTTPClient:       &http.Client{Timeout: 120 * time.Second},
	}
}

// Dimension returns the embedding dimension.
func (e *OllamaEmbedder) Dimension() int { return e.Dim }

// ModelName returns the model name.
func (e *OllamaEmbedder) ModelName() string { return e.Model }

// MaxBatchSize returns the maximum batch size.
func (e *OllamaEmbedder) MaxBatchSize() int { return e.MaxBatch }

// Embed generates embeddings using Ollama API.
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var all [][]float32

	for _, text := range texts {
		payload := map[string]interface{}{
			"model":  e.Model,
			"prompt": text,
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/api/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")

		resp, err := e.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}

		var result struct {
			Embedding []float64 `json:"embedding"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		v := make([]float32, len(result.Embedding))
		for i := range result.Embedding {
			v[i] = float32(result.Embedding[i])
		}

		// Normalize
		v = NormalizeL2(v)
		all = append(all, v)
	}

	return all, nil
}

// Helper functions

func estimateTokensLocal(s string) int {
	return len(s) / 4
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}

	if _, ok := err.(net.Error); ok {
		return true
	}

	msg := err.Error()
	for _, code := range []string{"429", "500", "502", "503", "504"} {
		if contains(msg, code) {
			return true
		}
	}

	if contains(msg, "timeout") || contains(msg, "Temporary") || contains(msg, "connection refused") {
		return true
	}

	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
