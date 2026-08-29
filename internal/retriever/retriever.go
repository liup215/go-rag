package retriever

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"sync"
	"unicode"

	"github.com/liup215/go-rag/internal/embedder"
	"github.com/liup215/go-rag/internal/storage"
)

// rrfK is the constant used in Reciprocal Rank Fusion (RRF).
// A value of 60 is recommended in the original RRF paper.
const rrfK = 60

// candidateMultiplier controls how many candidates each retriever fetches
// before RRF fusion.  Fetching more candidates improves recall at the cost of
// latency.  The final result set is always trimmed to TopK.
const candidateMultiplier = 10

// uncertainExpansionSuffix is appended when no query rewriter output is
// available. The phrase is Chinese ("detailed explanation") to better match
// the repository's current Chinese education use case, and is used in the
// UNCERTAIN corrective branch when expansion still only contains the original query.
const uncertainExpansionSuffix = " 详细解释"

// Retriever performs hybrid search on the knowledge base.
type Retriever struct {
	storage           storage.Storage
	embedder          embedder.Embedder
	reranker          Reranker
	qaEvaluator       QAEvaluator
	webSearcher       WebSearcher
	queryRewriter     QueryRewriter
	rewriteQueryLimit int
	threshold         float64
}

// NewRetriever creates a new Retriever.
func NewRetriever(store storage.Storage, emb embedder.Embedder) *Retriever {
	return &Retriever{
		storage:           store,
		embedder:          emb,
		rewriteQueryLimit: 3,
		threshold:         0.5, // Default similarity threshold
	}
}

// SetThreshold sets the minimum similarity score for results.
func (r *Retriever) SetThreshold(threshold float64) {
	r.threshold = threshold
}

// SetReranker sets the reranker used to re-score results after initial
// retrieval.  Pass nil to disable reranking.
func (r *Retriever) SetReranker(rr Reranker) {
	r.reranker = rr
}

// SetQAEvaluator sets the retrieval quality evaluator used by Corrective RAG.
func (r *Retriever) SetQAEvaluator(e QAEvaluator) {
	r.qaEvaluator = e
}

// SetWebSearcher sets the web search fallback used when retrieval quality is low.
func (r *Retriever) SetWebSearcher(ws WebSearcher) {
	r.webSearcher = ws
}

// SetQueryRewriter sets query rewriting and multi-query expansion behavior.
func (r *Retriever) SetQueryRewriter(qr QueryRewriter, maxQueries int) {
	r.queryRewriter = qr
	if maxQueries > 0 {
		r.rewriteQueryLimit = maxQueries
	}
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
//
// When a Reranker is configured, Search fetches a wider candidate pool
// (TopK * rerankerCandidateMultiplier) before reranking and trimming to TopK.
// If the reranker call fails, Search falls back to the original retrieval order
// without propagating the error.
func (r *Retriever) Search(ctx context.Context, opts SearchOptions) ([]storage.SearchResult, error) {
	opts = r.normalizeSearchOptions(opts)

	queries := []string{opts.Query}
	if r.queryRewriter != nil {
		rewritten, err := r.queryRewriter.Rewrite(ctx, opts.Query, r.rewriteQueryLimit)
		if err == nil {
			queries = appendUniqueStrings(queries, rewritten...)
		}
	}

	results, err := r.searchAcrossQueries(ctx, opts, queries)
	if err != nil {
		return nil, err
	}

	return r.applyCorrectiveStrategy(ctx, opts, results)
}

func (r *Retriever) normalizeSearchOptions(opts SearchOptions) SearchOptions {
	if opts.TopK <= 0 {
		opts.TopK = 5
	}
	if opts.Threshold == 0 {
		opts.Threshold = r.threshold
	}
	return opts
}

func (r *Retriever) searchAcrossQueries(
	ctx context.Context,
	opts SearchOptions,
	queries []string,
) ([]storage.SearchResult, error) {
	if len(queries) <= 1 {
		return r.searchSingle(ctx, opts)
	}

	resultsByQuery := make([][]storage.SearchResult, len(queries))
	errs := make([]error, len(queries))

	var wg sync.WaitGroup
	for i := range queries {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			queryOpts := opts
			queryOpts.Query = queries[idx]
			resultsByQuery[idx], errs[idx] = r.searchSingle(ctx, queryOpts)
		}(i)
	}
	wg.Wait()

	var merged []storage.SearchResult
	var firstErr error
	for i := range errs {
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		merged = append(merged, resultsByQuery[i]...)
	}

	if len(merged) == 0 && firstErr != nil {
		return nil, firstErr
	}

	return dedupeAndSortResults(merged, opts.TopK), nil
}

func (r *Retriever) searchSingle(ctx context.Context, opts SearchOptions) ([]storage.SearchResult, error) {
	// When a reranker is set, fetch more candidates so the cross-encoder has
	// a richer pool to re-score before we trim to the requested TopK.
	candidateOpts := opts
	if r.reranker != nil {
		expanded := opts.TopK * rerankerCandidateMultiplier
		if expanded > candidateOpts.TopK {
			candidateOpts.TopK = expanded
		}
	}

	var results []storage.SearchResult
	var err error
	if r.embedder != nil {
		results, err = r.hybridSearch(ctx, candidateOpts)
	} else {
		results, err = r.keywordSearch(candidateOpts)
	}
	if err != nil {
		return nil, err
	}

	// Apply reranker when configured; fall back to retrieval order on error.
	if r.reranker != nil && len(results) > 0 {
		reranked, rerankErr := r.reranker.Rerank(ctx, opts.Query, results)
		if rerankErr == nil {
			results = reranked
		}
		// Trim to the originally requested TopK regardless of reranker outcome.
		if len(results) > opts.TopK {
			results = results[:opts.TopK]
		}
	}

	return results, nil
}

func (r *Retriever) applyCorrectiveStrategy(
	ctx context.Context,
	opts SearchOptions,
	results []storage.SearchResult,
) ([]storage.SearchResult, error) {
	if r.qaEvaluator == nil {
		return results, nil
	}

	quality, err := r.qaEvaluator.Evaluate(ctx, opts.Query, results)
	if err != nil {
		return results, nil
	}

	switch quality {
	case RetrievalQualityHigh:
		return results, nil
	case RetrievalQualityLow:
		if r.webSearcher == nil {
			return results, nil
		}
		webResults, err := r.webSearcher.Search(ctx, opts.Query, opts.TopK)
		if err != nil {
			return results, nil
		}
		return dedupeAndSortResults(append(results, webResults...), opts.TopK), nil
	case RetrievalQualityUncertain:
		expanded := []string{opts.Query}
		if r.queryRewriter != nil {
			rewritten, err := r.queryRewriter.Rewrite(ctx, opts.Query, r.rewriteQueryLimit)
			if err == nil {
				expanded = appendUniqueStrings(expanded, rewritten...)
			}
		}
		if len(expanded) == 1 {
			expanded = append(expanded, opts.Query+uncertainExpansionSuffix)
		}
		expandedResults, err := r.searchAcrossQueries(ctx, opts, expanded)
		if err != nil {
			return results, nil
		}
		return dedupeAndSortResults(append(results, expandedResults...), opts.TopK), nil
	default:
		return results, nil
	}
}

func dedupeAndSortResults(results []storage.SearchResult, topK int) []storage.SearchResult {
	byChunk := make(map[string]storage.SearchResult, len(results))
	for _, res := range results {
		key := res.Chunk.ID
		if key == "" {
			key = fmt.Sprintf("%s|%x", res.Chunk.DocumentID, hashString64(res.Chunk.Text))
		}
		if old, ok := byChunk[key]; !ok || res.Score > old.Score {
			byChunk[key] = res
		}
	}

	merged := make([]storage.SearchResult, 0, len(byChunk))
	for _, res := range byChunk {
		merged = append(merged, res)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
}

func hashString64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func appendUniqueStrings(base []string, values ...string) []string {
	seen := make(map[string]struct{}, len(base))
	for _, v := range base {
		seen[v] = struct{}{}
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		base = append(base, v)
	}
	return base
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
//
// ASCII words behave exactly as before: a run of [0-9A-Za-z] forms one
// token, lower-cased.  CJK text (Chinese/Japanese/Korean) is written without
// spaces, so a contiguous CJK run is additionally split into overlapping
// bigrams with unigrams layered on top.  Without this a whole Chinese
// sentence - sometimes a whole chunk - collapsed into a single token and
// BM25 could only match when the query equalled that exact run, so Chinese
// keyword recall was effectively zero.
func tokenize(s string) []string {
	words := splitWords(s)
	for i := range words {
		words[i] = normalizeWord(words[i])
	}
	return filterEmpty(words)
}

// Helper functions

// splitWords splits text into word tokens.  Anything that is not a word rune
// (whitespace, ASCII punctuation and CJK punctuation such as '，' and '。')
// acts as a token separator; CJK word runs are then expanded into n-grams by
// splitWordRun.
func splitWords(s string) []string {
	var words []string
	var current []rune

	flush := func() {
		if len(current) == 0 {
			return
		}
		words = append(words, splitWordRun(current)...)
		current = current[:0]
	}

	for _, r := range s {
		if isWordChar(r) {
			current = append(current, r)
		} else {
			flush()
		}
	}
	flush()

	return words
}

// splitWordRun turns one contiguous run of word runes into tokens.  The run
// is first partitioned into maximal CJK and non-CJK sub-runs: CJK sub-runs
// become n-grams, while every other sub-run (ASCII words, accented Latin,
// ...) is kept whole so that e.g. "GPT4模型" yields "gpt4" plus the n-grams
// of "模型".
func splitWordRun(rs []rune) []string {
	var words []string
	start := 0
	for i := 1; i <= len(rs); i++ {
		if i == len(rs) || isCJKRune(rs[i]) != isCJKRune(rs[start]) {
			if sub := rs[start:i]; isCJKRune(sub[0]) {
				words = append(words, cjkNgrams(sub)...)
			} else {
				words = append(words, string(sub))
			}
			start = i
		}
	}
	return words
}

// cjkNgrams splits a run of CJK characters into unigrams overlaid with
// overlapping bigrams: "机器学习" yields "机", "机器", "器", "器学",
// "学", "学习", "习".  Bigrams carry most of the discriminative power,
// because single Chinese characters are highly ambiguous; unigrams keep
// one-character queries matchable.
func cjkNgrams(rs []rune) []string {
	tokens := make([]string, 0, 2*len(rs))
	for i, r := range rs {
		tokens = append(tokens, string(r))
		if i+1 < len(rs) {
			tokens = append(tokens, string(r)+string(rs[i+1]))
		}
	}
	return tokens
}

// isCJKRune reports whether r belongs to a CJK script (Han ideographs,
// Kana or Hangul syllables).  These scripts are written without spaces and
// therefore need n-gram tokenisation instead of word splitting.
func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// isWordChar reports whether r belongs to a word.  ASCII alphanumerics always
// do.  Non-ASCII runes do as well, unless they are punctuation, symbols,
// spaces or control characters - which is what makes CJK punctuation
// ('，' '。' '《》') separate tokens instead of gluing Chinese text into one
// giant token.
func isWordChar(r rune) bool {
	if r < 128 {
		return (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9')
	}
	return !unicode.IsPunct(r) && !unicode.IsSymbol(r) &&
		!unicode.IsSpace(r) && !unicode.IsControl(r)
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
