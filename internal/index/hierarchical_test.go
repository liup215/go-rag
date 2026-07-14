package index

import (
	"context"
	"testing"

	"github.com/user/go-rag/internal/storage"
)

// MockEmbedder is a mock implementation of embedder.Embedder for testing.
type MockEmbedder struct{}

func (m *MockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	// Return simple embeddings for testing
	result := make([][]float32, len(texts))
	for i := range texts {
		// Create a simple embedding based on text length
		embedding := make([]float32, 10)
		textLen := float32(len(texts[i]))
		for j := range embedding {
			embedding[j] = textLen / float32(j+1)
		}
		result[i] = embedding
	}
	return result, nil
}

func (m *MockEmbedder) Dimension() int {
	return 10
}

func (m *MockEmbedder) ModelName() string {
	return "mock"
}

func (m *MockEmbedder) MaxBatchSize() int {
	return 100
}

// MockLLMClient is a mock implementation for testing.
type MockLLMClient struct{}

func (m *MockLLMClient) Complete(ctx context.Context, prompt string) (string, error) {
	return "This is a test summary of the document.", nil
}

func (m *MockLLMClient) CompleteWithJSON(ctx context.Context, prompt string) (map[string]interface{}, error) {
	return nil, nil
}

func TestNewHierarchicalIndex(t *testing.T) {
	embedder := &MockEmbedder{}
	llmClient := &MockLLMClient{}

	index := NewHierarchicalIndex(embedder, llmClient)
	if index == nil {
		t.Fatal("Expected non-nil index")
	}

	if index.embedder == nil {
		t.Error("Expected embedder to be set")
	}

	if index.llmClient == nil {
		t.Error("Expected llmClient to be set")
	}
}

func TestHierarchicalIndex_AddNode(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})

	node := &HierarchicalNode{
		ID:         "test_node",
		DocumentID: "doc1",
		Layer:      LayerSummary,
		Text:       "Test summary",
		Embedding:  []float32{1.0, 2.0, 3.0},
	}

	index.AddNode(node)

	retrieved, exists := index.GetNode("test_node")
	if !exists {
		t.Fatal("Node not found after adding")
	}

	if retrieved.ID != "test_node" {
		t.Errorf("Expected ID 'test_node', got '%s'", retrieved.ID)
	}

	if retrieved.Text != "Test summary" {
		t.Errorf("Expected text 'Test summary', got '%s'", retrieved.Text)
	}
}

func TestHierarchicalIndex_GetDocumentNodes(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})

	// Add multiple nodes for same document
	index.AddNode(&HierarchicalNode{
		ID:         "doc1_summary",
		DocumentID: "doc1",
		Layer:      LayerSummary,
		Text:       "Summary",
	})

	index.AddNode(&HierarchicalNode{
		ID:         "doc1_para_1",
		DocumentID: "doc1",
		Layer:      LayerParagraph,
		Text:       "Paragraph 1",
	})

	index.AddNode(&HierarchicalNode{
		ID:         "doc1_para_2",
		DocumentID: "doc1",
		Layer:      LayerParagraph,
		Text:       "Paragraph 2",
	})

	// Get summary layer nodes
	summaryNodes := index.GetDocumentNodes("doc1", LayerSummary)
	if len(summaryNodes) != 1 {
		t.Errorf("Expected 1 summary node, got %d", len(summaryNodes))
	}

	// Get paragraph layer nodes
	paraNodes := index.GetDocumentNodes("doc1", LayerParagraph)
	if len(paraNodes) != 2 {
		t.Errorf("Expected 2 paragraph nodes, got %d", len(paraNodes))
	}
}

func TestHierarchicalIndex_GetAllNodesAtLayer(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})

	// Add nodes at different layers
	index.AddNode(&HierarchicalNode{
		ID:         "doc1_summary",
		DocumentID: "doc1",
		Layer:      LayerSummary,
		Text:       "Summary 1",
	})

	index.AddNode(&HierarchicalNode{
		ID:         "doc2_summary",
		DocumentID: "doc2",
		Layer:      LayerSummary,
		Text:       "Summary 2",
	})

	index.AddNode(&HierarchicalNode{
		ID:         "doc1_para",
		DocumentID: "doc1",
		Layer:      LayerParagraph,
		Text:       "Paragraph",
	})

	summaryNodes := index.GetAllNodesAtLayer(LayerSummary)
	if len(summaryNodes) != 2 {
		t.Errorf("Expected 2 summary nodes, got %d", len(summaryNodes))
	}

	paraNodes := index.GetAllNodesAtLayer(LayerParagraph)
	if len(paraNodes) != 1 {
		t.Errorf("Expected 1 paragraph node, got %d", len(paraNodes))
	}
}

func TestHierarchicalIndex_SplitIntoParagraphs(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})
	opts := DefaultBuildOptions()

	text := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	paragraphs := index.splitIntoParagraphs(text, opts)

	if len(paragraphs) < 3 {
		t.Errorf("Expected at least 3 paragraphs, got %d", len(paragraphs))
	}
}

func TestHierarchicalIndex_SplitIntoSentences(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})
	opts := DefaultBuildOptions()

	text := "First sentence. Second sentence! Third sentence?"
	sentences := index.splitIntoSentences(text, opts)

	if len(sentences) < 3 {
		t.Errorf("Expected at least 3 sentences, got %d", len(sentences))
	}
}

func TestHierarchicalIndex_BuildFromChunks(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})
	opts := DefaultBuildOptions()

	chunks := []storage.Chunk{
		{
			ID:         "chunk1",
			DocumentID: "doc1",
			Text:       "This is a test document. It has multiple sentences. This is for testing hierarchical indexing.",
		},
	}

	ctx := context.Background()
	err := index.BuildFromChunks(ctx, chunks, opts)
	if err != nil {
		t.Fatalf("Failed to build from chunks: %v", err)
	}

	// Check that summary layer was created
	summaryNodes := index.GetDocumentNodes("doc1", LayerSummary)
	if len(summaryNodes) == 0 {
		t.Error("Expected summary node to be created")
	}

	// Check that paragraph layer was created
	paraNodes := index.GetDocumentNodes("doc1", LayerParagraph)
	if len(paraNodes) == 0 {
		t.Error("Expected paragraph nodes to be created")
	}

	// Check that sentence layer was created
	sentNodes := index.GetDocumentNodes("doc1", LayerSentence)
	if len(sentNodes) == 0 {
		t.Error("Expected sentence nodes to be created")
	}
}

func TestHierarchicalIndex_SearchNodes(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})

	// Create test nodes with embeddings
	nodes := []*HierarchicalNode{
		{
			ID:        "node1",
			Text:      "Test node 1",
			Embedding: []float32{1.0, 2.0, 3.0},
		},
		{
			ID:        "node2",
			Text:      "Test node 2",
			Embedding: []float32{2.0, 3.0, 4.0},
		},
		{
			ID:        "node3",
			Text:      "Test node 3",
			Embedding: []float32{3.0, 4.0, 5.0},
		},
	}

	queryEmbedding := []float32{1.5, 2.5, 3.5}
	results := index.searchNodes(queryEmbedding, nodes, 2, 0.0)

	if len(results) > 2 {
		t.Errorf("Expected at most 2 results, got %d", len(results))
	}

	// Check that results are sorted by score
	if len(results) >= 2 && results[0].Score < results[1].Score {
		t.Error("Expected results to be sorted by score descending")
	}
}

func TestHierarchicalIndex_DeduplicateResults(t *testing.T) {
	index := NewHierarchicalIndex(&MockEmbedder{}, &MockLLMClient{})

	results := []SearchResult{
		{
			Node: &HierarchicalNode{
				DocumentID: "doc1",
				Text:       "Test text",
			},
			Score: 0.9,
		},
		{
			Node: &HierarchicalNode{
				DocumentID: "doc1",
				Text:       "Test text",
			},
			Score: 0.8,
		},
		{
			Node: &HierarchicalNode{
				DocumentID: "doc2",
				Text:       "Different text",
			},
			Score: 0.7,
		},
	}

	deduped := index.deduplicateResults(results)
	if len(deduped) != 2 {
		t.Errorf("Expected 2 unique results, got %d", len(deduped))
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float64
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 1.0,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1.0, 0.0},
			b:        []float32{0.0, 1.0},
			expected: 0.0,
		},
		{
			name:     "different length vectors",
			a:        []float32{1.0, 2.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 0.0,
		},
		{
			name:     "empty vectors",
			a:        []float32{},
			b:        []float32{},
			expected: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.a, tt.b)

			// For identical vectors, result should be close to 1.0
			if tt.name == "identical vectors" && result < 0.99 {
				t.Errorf("Expected similarity close to 1.0, got %f", result)
			}

			// For orthogonal vectors, result should be close to 0.0
			if tt.name == "orthogonal vectors" && result > 0.01 {
				t.Errorf("Expected similarity close to 0.0, got %f", result)
			}

			// For invalid cases, result should be 0.0
			if (tt.name == "different length vectors" || tt.name == "empty vectors") && result != 0.0 {
				t.Errorf("Expected 0.0 for %s, got %f", tt.name, result)
			}
		})
	}
}

func TestDefaultBuildOptions(t *testing.T) {
	opts := DefaultBuildOptions()

	if opts.MaxSummaryLength <= 0 {
		t.Error("Expected positive MaxSummaryLength")
	}

	if opts.MaxParagraphLength <= 0 {
		t.Error("Expected positive MaxParagraphLength")
	}

	if opts.MaxSentenceLength <= 0 {
		t.Error("Expected positive MaxSentenceLength")
	}

	if opts.SentenceSplitChars == "" {
		t.Error("Expected non-empty SentenceSplitChars")
	}
}
