package index

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/user/go-rag/internal/embedder"
	"github.com/user/go-rag/internal/llm"
	"github.com/user/go-rag/internal/storage"
)

// Layer represents a level in the hierarchical index.
type Layer int

const (
	// LayerSummary is the top layer containing document summaries.
	LayerSummary Layer = iota
	// LayerParagraph is the middle layer containing paragraphs.
	LayerParagraph
	// LayerSentence is the bottom layer containing individual sentences.
	LayerSentence
)

// HierarchicalNode represents a node in the hierarchical index.
type HierarchicalNode struct {
	ID         string
	DocumentID string
	Layer      Layer
	Text       string
	Embedding  []float32
	Parent     string   // Parent node ID
	Children   []string // Child node IDs
	Metadata   map[string]string
}

// HierarchicalIndex stores a multi-layer document index.
type HierarchicalIndex struct {
	mu        sync.RWMutex
	nodes     map[string]*HierarchicalNode
	embedder  embedder.Embedder
	llmClient llm.Client

	// Index by layer and document for efficient access
	documentNodes map[string]map[Layer][]*HierarchicalNode
}

// NewHierarchicalIndex creates a new hierarchical index.
func NewHierarchicalIndex(emb embedder.Embedder, llmClient llm.Client) *HierarchicalIndex {
	return &HierarchicalIndex{
		nodes:         make(map[string]*HierarchicalNode),
		embedder:      emb,
		llmClient:     llmClient,
		documentNodes: make(map[string]map[Layer][]*HierarchicalNode),
	}
}

// AddNode adds a node to the index.
func (hi *HierarchicalIndex) AddNode(node *HierarchicalNode) {
	hi.mu.Lock()
	defer hi.mu.Unlock()

	hi.nodes[node.ID] = node

	// Update document index
	if _, exists := hi.documentNodes[node.DocumentID]; !exists {
		hi.documentNodes[node.DocumentID] = make(map[Layer][]*HierarchicalNode)
	}
	hi.documentNodes[node.DocumentID][node.Layer] = append(
		hi.documentNodes[node.DocumentID][node.Layer],
		node,
	)
}

// GetNode retrieves a node by ID.
func (hi *HierarchicalIndex) GetNode(id string) (*HierarchicalNode, bool) {
	hi.mu.RLock()
	defer hi.mu.RUnlock()

	node, exists := hi.nodes[id]
	return node, exists
}

// GetDocumentNodes returns all nodes for a document at a specific layer.
func (hi *HierarchicalIndex) GetDocumentNodes(documentID string, layer Layer) []*HierarchicalNode {
	hi.mu.RLock()
	defer hi.mu.RUnlock()

	if layers, exists := hi.documentNodes[documentID]; exists {
		return layers[layer]
	}
	return nil
}

// GetAllNodesAtLayer returns all nodes at a specific layer.
func (hi *HierarchicalIndex) GetAllNodesAtLayer(layer Layer) []*HierarchicalNode {
	hi.mu.RLock()
	defer hi.mu.RUnlock()

	var nodes []*HierarchicalNode
	for _, node := range hi.nodes {
		if node.Layer == layer {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// BuildOptions contains options for building the hierarchical index.
type BuildOptions struct {
	MaxSummaryLength    int
	MaxParagraphLength  int
	MaxSentenceLength   int
	SentenceSplitChars  string
	ParagraphSplitChars string
}

// DefaultBuildOptions returns default build options.
func DefaultBuildOptions() BuildOptions {
	return BuildOptions{
		MaxSummaryLength:    500,
		MaxParagraphLength:  2000,
		MaxSentenceLength:   200,
		SentenceSplitChars:  ".!?。！？",
		ParagraphSplitChars: "\n\n",
	}
}

// BuildFromChunks builds a hierarchical index from document chunks.
func (hi *HierarchicalIndex) BuildFromChunks(ctx context.Context, chunks []storage.Chunk, opts BuildOptions) error {
	// Group chunks by document
	chunksByDoc := make(map[string][]storage.Chunk)
	for _, chunk := range chunks {
		chunksByDoc[chunk.DocumentID] = append(chunksByDoc[chunk.DocumentID], chunk)
	}

	// Process each document
	for docID, docChunks := range chunksByDoc {
		if err := hi.buildDocumentHierarchy(ctx, docID, docChunks, opts); err != nil {
			return fmt.Errorf("failed to build hierarchy for document %s: %w", docID, err)
		}
	}

	return nil
}

// buildDocumentHierarchy builds the hierarchy for a single document.
func (hi *HierarchicalIndex) buildDocumentHierarchy(
	ctx context.Context,
	documentID string,
	chunks []storage.Chunk,
	opts BuildOptions,
) error {
	// Combine all chunks into full document text
	var fullText strings.Builder
	for _, chunk := range chunks {
		fullText.WriteString(chunk.Text)
		fullText.WriteString("\n")
	}
	docText := fullText.String()

	// Layer 1: Generate summary
	summary, err := hi.generateSummary(ctx, docText, opts.MaxSummaryLength)
	if err != nil {
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	summaryEmbedding, err := hi.embedText(ctx, summary)
	if err != nil {
		return fmt.Errorf("failed to embed summary: %w", err)
	}

	summaryNode := &HierarchicalNode{
		ID:         fmt.Sprintf("%s_summary", documentID),
		DocumentID: documentID,
		Layer:      LayerSummary,
		Text:       summary,
		Embedding:  summaryEmbedding,
		Children:   []string{},
		Metadata:   make(map[string]string),
	}
	hi.AddNode(summaryNode)

	// Layer 2: Split into paragraphs
	paragraphs := hi.splitIntoParagraphs(docText, opts)
	for i, para := range paragraphs {
		if strings.TrimSpace(para) == "" {
			continue
		}

		paraEmbedding, err := hi.embedText(ctx, para)
		if err != nil {
			continue // Skip on error
		}

		paraID := fmt.Sprintf("%s_para_%d", documentID, i)
		paraNode := &HierarchicalNode{
			ID:         paraID,
			DocumentID: documentID,
			Layer:      LayerParagraph,
			Text:       para,
			Embedding:  paraEmbedding,
			Parent:     summaryNode.ID,
			Children:   []string{},
			Metadata:   make(map[string]string),
		}
		hi.AddNode(paraNode)
		summaryNode.Children = append(summaryNode.Children, paraID)

		// Layer 3: Split into sentences
		sentences := hi.splitIntoSentences(para, opts)
		for j, sent := range sentences {
			if strings.TrimSpace(sent) == "" {
				continue
			}

			sentEmbedding, err := hi.embedText(ctx, sent)
			if err != nil {
				continue // Skip on error
			}

			sentID := fmt.Sprintf("%s_para_%d_sent_%d", documentID, i, j)
			sentNode := &HierarchicalNode{
				ID:         sentID,
				DocumentID: documentID,
				Layer:      LayerSentence,
				Text:       sent,
				Embedding:  sentEmbedding,
				Parent:     paraID,
				Metadata:   make(map[string]string),
			}
			hi.AddNode(sentNode)
			paraNode.Children = append(paraNode.Children, sentID)
		}
	}

	return nil
}

// generateSummary generates a summary of the document using LLM.
func (hi *HierarchicalIndex) generateSummary(ctx context.Context, text string, maxLength int) (string, error) {
	if hi.llmClient == nil {
		// If no LLM client, use first N characters as summary
		if len(text) > maxLength {
			return text[:maxLength], nil
		}
		return text, nil
	}

	prompt := fmt.Sprintf(`Summarize the following text in a concise way (max %d characters):

%s

Summary:`, maxLength, text)

	summary, err := hi.llmClient.Complete(ctx, prompt)
	if err != nil {
		// Fallback to truncation on error
		if len(text) > maxLength {
			return text[:maxLength], nil
		}
		return text, nil
	}

	return strings.TrimSpace(summary), nil
}

// embedText embeds a text string.
func (hi *HierarchicalIndex) embedText(ctx context.Context, text string) ([]float32, error) {
	if hi.embedder == nil {
		return nil, fmt.Errorf("no embedder configured")
	}

	embeddings, err := hi.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}

	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("empty embedding returned")
	}

	return embeddings[0], nil
}

// splitIntoParagraphs splits text into paragraphs.
func (hi *HierarchicalIndex) splitIntoParagraphs(text string, opts BuildOptions) []string {
	// Split by double newline or other paragraph markers
	paragraphs := strings.Split(text, opts.ParagraphSplitChars)

	var result []string
	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para != "" {
			// Further split long paragraphs
			if len(para) > opts.MaxParagraphLength {
				chunks := hi.chunkText(para, opts.MaxParagraphLength)
				result = append(result, chunks...)
			} else {
				result = append(result, para)
			}
		}
	}

	return result
}

// splitIntoSentences splits text into sentences.
func (hi *HierarchicalIndex) splitIntoSentences(text string, opts BuildOptions) []string {
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])

		// Check if current rune is a sentence ending character
		if strings.ContainsRune(opts.SentenceSplitChars, runes[i]) {
			// Look ahead to see if this is really the end of a sentence
			if i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\n') {
				sentence := strings.TrimSpace(current.String())
				if sentence != "" {
					sentences = append(sentences, sentence)
				}
				current.Reset()
			}
		}
	}

	// Add remaining text as last sentence
	if current.Len() > 0 {
		sentence := strings.TrimSpace(current.String())
		if sentence != "" {
			sentences = append(sentences, sentence)
		}
	}

	return sentences
}

// chunkText splits text into chunks of max length.
func (hi *HierarchicalIndex) chunkText(text string, maxLength int) []string {
	var chunks []string
	runes := []rune(text)

	for i := 0; i < len(runes); i += maxLength {
		end := i + maxLength
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}

	return chunks
}

// SearchOptions contains options for hierarchical search.
type SearchOptions struct {
	Query     string
	TopK      int
	Threshold float64
}

// SearchResult represents a search result with score and layer information.
type SearchResult struct {
	Node  *HierarchicalNode
	Score float64
}

// HierarchicalSearch performs a hierarchical search strategy.
func (hi *HierarchicalIndex) HierarchicalSearch(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if hi.embedder == nil {
		return nil, fmt.Errorf("no embedder configured")
	}

	// Embed query
	queryEmbedding, err := hi.embedText(ctx, opts.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// Stage 1: Coarse recall at summary layer
	summaryNodes := hi.GetAllNodesAtLayer(LayerSummary)
	topSummaries := hi.searchNodes(queryEmbedding, summaryNodes, opts.TopK, opts.Threshold)

	// Stage 2: Fine-grained search at paragraph layer
	var candidateParagraphs []*HierarchicalNode
	for _, result := range topSummaries {
		// Get all paragraph children of selected summaries
		for _, childID := range result.Node.Children {
			if child, exists := hi.GetNode(childID); exists && child.Layer == LayerParagraph {
				candidateParagraphs = append(candidateParagraphs, child)
			}
		}
	}

	topParagraphs := hi.searchNodes(queryEmbedding, candidateParagraphs, opts.TopK*2, opts.Threshold)

	// Stage 3: Detailed search at sentence layer
	var candidateSentences []*HierarchicalNode
	for _, result := range topParagraphs {
		// Get all sentence children of selected paragraphs
		for _, childID := range result.Node.Children {
			if child, exists := hi.GetNode(childID); exists && child.Layer == LayerSentence {
				candidateSentences = append(candidateSentences, child)
			}
		}
	}

	topSentences := hi.searchNodes(queryEmbedding, candidateSentences, opts.TopK, opts.Threshold)

	// Combine results from all layers, prioritizing more specific layers
	var finalResults []SearchResult

	// Add sentence results with highest priority
	for _, result := range topSentences {
		finalResults = append(finalResults, result)
	}

	// Add paragraph results if we don't have enough
	if len(finalResults) < opts.TopK {
		for _, result := range topParagraphs {
			if len(finalResults) >= opts.TopK {
				break
			}
			// Slightly reduce score for paragraph results
			result.Score *= 0.95
			finalResults = append(finalResults, result)
		}
	}

	// Sort by score
	sort.Slice(finalResults, func(i, j int) bool {
		return finalResults[i].Score > finalResults[j].Score
	})

	// Deduplicate and trim to TopK
	finalResults = hi.deduplicateResults(finalResults)
	if len(finalResults) > opts.TopK {
		finalResults = finalResults[:opts.TopK]
	}

	return finalResults, nil
}

// searchNodes searches a list of nodes and returns top results.
func (hi *HierarchicalIndex) searchNodes(
	queryEmbedding []float32,
	nodes []*HierarchicalNode,
	topK int,
	threshold float64,
) []SearchResult {
	var results []SearchResult

	for _, node := range nodes {
		if len(node.Embedding) == 0 {
			continue
		}

		score := cosineSimilarity(queryEmbedding, node.Embedding)
		if score >= threshold {
			results = append(results, SearchResult{
				Node:  node,
				Score: score,
			})
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Trim to topK
	if len(results) > topK {
		results = results[:topK]
	}

	return results
}

// deduplicateResults removes duplicate results based on document ID and text.
func (hi *HierarchicalIndex) deduplicateResults(results []SearchResult) []SearchResult {
	seen := make(map[string]bool)
	var deduped []SearchResult

	for _, result := range results {
		key := result.Node.DocumentID + "|" + result.Node.Text
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, result)
		}
	}

	return deduped
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

	// Calculate sqrt separately for better numerical stability
	denominator := 1.0
	if na > 0 && nb > 0 {
		denominator = 1.0
		sqrtNa := na
		sqrtNb := nb
		
		// Simple iterative sqrt approximation
		for i := 0; i < 10; i++ {
			sqrtNa = (sqrtNa + na/sqrtNa) / 2
			sqrtNb = (sqrtNb + nb/sqrtNb) / 2
		}
		denominator = sqrtNa * sqrtNb
	}

	if denominator == 0 {
		return 0
	}

	return dot / denominator
}
