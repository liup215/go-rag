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
)

// QueryRewriter rewrites one user query into multiple retrieval-friendly queries.
type QueryRewriter interface {
	Rewrite(ctx context.Context, query string, maxQueries int) ([]string, error)
}

// RuleBasedQueryRewriter rewrites colloquial education queries using domain mappings.
type RuleBasedQueryRewriter struct {
	termMappings     map[string]string
	synonymMappings  map[string]string
	hyponymMappings  map[string][]string
}

// NewRuleBasedQueryRewriter creates a rule-based query rewriter.
func NewRuleBasedQueryRewriter() *RuleBasedQueryRewriter {
	return &RuleBasedQueryRewriter{
		termMappings: map[string]string{
			"能量工厂": "线粒体",
			"绿色工厂": "叶绿体",
		},
		synonymMappings: map[string]string{
			"光合作用": "photosynthesis",
		},
		hyponymMappings: map[string][]string{
			"细胞器": {"线粒体", "叶绿体"},
		},
	}
}

// Rewrite returns rewritten queries with dedupe and maxQueries limit.
func (r *RuleBasedQueryRewriter) Rewrite(
	_ context.Context,
	query string,
	maxQueries int,
) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if maxQueries <= 0 {
		maxQueries = 3
	}

	rewrites := make([]string, 0, maxQueries)

	for from, to := range r.termMappings {
		if strings.Contains(query, from) {
			rewrites = append(rewrites, strings.ReplaceAll(query, from, to))
		}
	}
	for from, to := range r.synonymMappings {
		if strings.Contains(query, from) {
			rewrites = append(rewrites, query+" "+to)
		}
	}
	for parent, children := range r.hyponymMappings {
		if strings.Contains(query, parent) {
			for _, child := range children {
				rewrites = append(rewrites, strings.ReplaceAll(query, parent, child))
			}
		}
	}

	rewrites = appendUniqueStrings(nil, rewrites...)
	if len(rewrites) > maxQueries {
		rewrites = rewrites[:maxQueries]
	}
	return rewrites, nil
}

// LLMQueryRewriter rewrites queries with an HTTP LLM endpoint.
type LLMQueryRewriter struct {
	url    string
	apiKey string
	model  string
	client *http.Client
}

type queryRewriteRequest struct {
	Model      string `json:"model,omitempty"`
	Query      string `json:"query"`
	MaxQueries int    `json:"max_queries"`
}

type queryRewriteResponse struct {
	Queries []string `json:"queries"`
}

// NewLLMQueryRewriter creates an LLM-based query rewriter.
func NewLLMQueryRewriter(url, apiKey, model string) *LLMQueryRewriter {
	return &LLMQueryRewriter{
		url:    url,
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// Rewrite calls the LLM endpoint and returns rewritten queries.
func (r *LLMQueryRewriter) Rewrite(
	ctx context.Context,
	query string,
	maxQueries int,
) ([]string, error) {
	if r.url == "" {
		return nil, fmt.Errorf("query rewriter URL is empty")
	}
	if maxQueries <= 0 {
		maxQueries = 3
	}

	body, err := json.Marshal(queryRewriteRequest{
		Model:      r.model,
		Query:      query,
		MaxQueries: maxQueries,
	})
	if err != nil {
		return nil, fmt.Errorf("query rewriter: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("query rewriter: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query rewriter: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("query rewriter: HTTP %d: %s", resp.StatusCode, snippet)
	}

	var out queryRewriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("query rewriter: decode response: %w", err)
	}

	queries := appendUniqueStrings(nil, out.Queries...)
	if len(queries) > maxQueries {
		queries = queries[:maxQueries]
	}
	return queries, nil
}

