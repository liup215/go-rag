package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
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

	// MaxAttempts limits retries for transient errors other than 429
	// (timeouts, 5xx, broken connections). A 429 rate limit is always
	// retried indefinitely — waiting is guaranteed to succeed eventually,
	// so giving up on it would only fail a doomed-to-succeed batch.
	// Zero means DefaultMaxAttempts.
	MaxAttempts int
	// RetryBackoff is the exponential backoff base (1s, 2s, 4s, ... capped
	// at MaxBackoff). Zero means DefaultRetryBackoff.
	RetryBackoff time.Duration
	// MaxBackoff caps the exponential backoff (the 429 Retry-After header,
	// if present, may still wait longer). Zero means DefaultMaxBackoff.
	MaxBackoff time.Duration
}

// Retry tuning defaults: non-429 transient errors are retried for up to
// ~2 minutes of pure backoff (1+2+4+8+16+30+30). 429 rate limits retry
// forever — the backoff keeps growing but the attempt count never triggers
// a give-up, so long rate-limit windows are waited out, not failed.
const (
	DefaultMaxAttempts  = 8
	DefaultRetryBackoff = time.Second
	DefaultMaxBackoff   = 30 * time.Second
)

// rateLimitError marks a 429 response, carrying the server-advised wait.
type rateLimitError struct {
	statusLine string
	body       string
	// retryAfter is the value of the Retry-After header (0 when absent).
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	if e.retryAfter > 0 {
		return fmt.Sprintf("embeddings API 429 (retry after %s): %s%s", e.retryAfter, e.statusLine, e.body)
	}
	return fmt.Sprintf("embeddings API 429: %s%s", e.statusLine, e.body)
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
	maxAttempts := e.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	backoff := e.RetryBackoff
	if backoff <= 0 {
		backoff = DefaultRetryBackoff
	}
	maxBackoff := e.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = DefaultMaxBackoff
	}

	grow := func() {
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	// attempt counts only non-429 retries; 429 loops forever.
	for attempt := 1; ; attempt++ {
		vecs, err := e.embedBatch(ctx, texts)
		if err == nil {
			return vecs, nil
		}

		var rlErr *rateLimitError
		if errors.As(err, &rlErr) {
			// Rate limited: always wait and retry, never give up. The
			// server-advised Retry-After wins when it is longer than our
			// own backoff, so a strict quota window is not re-hit.
			wait := backoff
			if rlErr.retryAfter > wait {
				wait = rlErr.retryAfter
			}
			fmt.Fprintf(os.Stderr, "⚠ embedding API 限速（第 %d 次等待），%s 后重试…\n",
				attempt, wait.Truncate(time.Millisecond))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			grow()
			continue
		}

		// Any other error: bounded retries, then give up.
		if !isRetryable(err) || attempt >= maxAttempts {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "⚠ embedding API 暂时不可用（%d/%d），%s 后重试…\n",
			attempt, maxAttempts, backoff.Truncate(time.Millisecond))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		grow()
	}
}

// retryAfter parses an HTTP Retry-After seconds value ("120"); it returns 0
// for anything it cannot parse. (Dates are tolerated by returning 0.)
func retryAfter(v string) time.Duration {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Second
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
		line := fmt.Sprintf("embeddings API %d: ", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, &rateLimitError{statusLine: line, body: string(b), retryAfter: retryAfter(resp.Header.Get("Retry-After"))}
		}
		return nil, fmt.Errorf("%s%s", line, string(b))
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
