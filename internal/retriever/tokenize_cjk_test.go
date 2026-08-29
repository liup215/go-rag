package retriever

import (
	"context"
	"slices"
	"testing"

	"github.com/liup215/go-rag/internal/storage"
)

// ---- CJK tokenisation tests -------------------------------------------------

// A contiguous run of Chinese characters must be split into overlapping
// bigrams (plus unigrams).  Regression: every rune > 127 used to count as a
// word character, so the whole run became a single token and BM25 could only
// match queries that equalled that exact run.
func TestTokenize_CJKRun_Bigrams(t *testing.T) {
	tokens := tokenize("机器学习算法")

	for _, want := range []string{"机器", "器学", "学习", "习算", "算法"} {
		if !slices.Contains(tokens, want) {
			t.Errorf("tokenize(%q) missing bigram %q; got %v", "机器学习算法", want, tokens)
		}
	}
	for _, want := range []string{"机", "器", "学", "习", "算", "法"} {
		if !slices.Contains(tokens, want) {
			t.Errorf("tokenize(%q) missing unigram %q; got %v", "机器学习算法", want, tokens)
		}
	}
	// The whole run must never survive as one token.
	if slices.Contains(tokens, "机器学习算法") {
		t.Errorf("tokenize(%q) still emits the whole CJK run as one token: %v", "机器学习算法", tokens)
	}
	for _, tok := range tokens {
		if n := len([]rune(tok)); n > 2 {
			t.Errorf("token %q is longer than a bigram (%d runes)", tok, n)
		}
	}
}

// Chinese punctuation must separate tokens instead of being glued to the
// surrounding text.
func TestTokenize_CJKPunctuationSeparates(t *testing.T) {
	tokens := tokenize("机器学习算法，深度学习模型。")

	if !slices.Contains(tokens, "算法") || !slices.Contains(tokens, "深度") {
		t.Errorf("expected 算法 and 深度 as separate tokens; got %v", tokens)
	}
	for _, unwanted := range []string{"，", "。", "法深", "法，", "，深", "型。"} {
		if slices.Contains(tokens, unwanted) {
			t.Errorf("tokenize should not emit %q; got %v", unwanted, tokens)
		}
	}
}

// A lone CJK character is kept as a unigram so single-character queries stay
// matchable.
func TestTokenize_CJKSingleChar(t *testing.T) {
	tokens := tokenize("光")
	if len(tokens) != 1 || tokens[0] != "光" {
		t.Errorf("tokenize(%q) = %v, want [光]", "光", tokens)
	}
}

// ASCII behaviour is unchanged, and ASCII words adjacent to CJK runs are kept
// whole instead of being merged into the CJK token.
func TestTokenize_ASCIIAndMixedScript(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"Hello World", []string{"hello", "world"}},
		{"machine-learning", []string{"machine", "learning"}},
		{"RAG检索增强", []string{"rag", "检索", "增强"}},
		{"GPT4模型", []string{"gpt4", "模型"}},
	}
	for _, tc := range cases {
		tokens := tokenize(tc.input)
		for _, want := range tc.want {
			if !slices.Contains(tokens, want) {
				t.Errorf("tokenize(%q) missing %q; got %v", tc.input, want, tokens)
			}
		}
		if slices.Contains(tokens, tc.input) {
			t.Errorf("tokenize(%q) must not keep the whole string as one token; got %v", tc.input, tokens)
		}
	}
}

// ---- BM25 with Chinese queries ----------------------------------------------

func TestBM25Index_ChineseQueryMatches(t *testing.T) {
	chunks := []storage.Chunk{
		makeChunk("1", "d1", "本章介绍机器学习算法的基本概念与常见模型。"),
		makeChunk("2", "d1", "今天天气很好，我们去公园散步。"),
		makeChunk("3", "d1", "The quick brown fox jumps over the lazy dog."),
	}

	idx := BuildBM25Index(chunks)
	results := idx.Search("机器学习", 3)

	if len(results) == 0 {
		t.Fatal("expected the Chinese query to match, got none")
	}
	if results[0].chunk.ID != "1" {
		t.Errorf("expected the 机器学习 chunk as top result, got chunk %s", results[0].chunk.ID)
	}
}

// A single Chinese character must still find chunks containing it (unigram
// layer), even when it only appears inside longer runs.
func TestBM25Index_ChineseSingleCharQuery(t *testing.T) {
	chunks := []storage.Chunk{
		makeChunk("1", "d1", "机器学习算法的基础知识。"),
		makeChunk("2", "d1", "红烧肉的做法很简单。"),
	}

	idx := BuildBM25Index(chunks)
	results := idx.Search("学", 2)

	if len(results) == 0 {
		t.Fatal("expected the chunk containing 学 to match, got none")
	}
	if results[0].chunk.ID != "1" {
		t.Errorf("expected chunk 1 (contains 学), got chunk %s", results[0].chunk.ID)
	}
}

// ---- Retriever-level Chinese search ------------------------------------------

func TestRetriever_KeywordSearch_Chinese(t *testing.T) {
	store := &mockStorage{
		chunks: []storage.Chunk{
			makeChunk("1", "d1", "机器学习算法是人工智能的核心技术之一。"),
			makeChunk("2", "d1", "今天的晚餐是番茄炒蛋。"),
		},
	}

	ret := NewRetriever(store, nil)
	ret.SetThreshold(0)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "机器学习",
		TopK:  2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	if results[0].Chunk.ID != "1" {
		t.Errorf("expected the 机器学习 chunk as top result, got chunk %s", results[0].Chunk.ID)
	}
}

// In hybrid mode the keyword branch must surface the relevant chunk even when
// the embedder cannot distinguish the candidates (the mock returns the same
// vector for every input, so only BM25 can tell the chunks apart).
func TestRetriever_HybridSearch_ChineseKeywordBranch(t *testing.T) {
	vec := []float32{1, 0}
	chunks := []storage.Chunk{
		{ID: "1", DocumentID: "d1", Text: "机器学习算法是人工智能的核心。", Embedding: []float32{1, 0}},
		{ID: "2", DocumentID: "d1", Text: "今天晚餐吃番茄炒蛋。", Embedding: []float32{1, 0}},
	}

	store := &mockStorage{chunks: chunks}
	emb := &mockEmbedder{vec: vec}

	ret := NewRetriever(store, emb)
	ret.SetThreshold(0)

	results, err := ret.Search(context.Background(), SearchOptions{
		Query: "机器学习",
		TopK:  2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	if results[0].Chunk.ID != "1" {
		t.Errorf("expected BM25 to push the 机器学习 chunk to the top, got chunk %s", results[0].Chunk.ID)
	}
}
