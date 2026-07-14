package knowledge

import (
	"context"
	"strings"
	"testing"

	"github.com/user/go-rag/internal/storage"
)

// MockLLMClient is a mock implementation of llm.Client for testing.
type MockLLMClient struct {
	responses map[string]string
}

func NewMockLLMClient() *MockLLMClient {
	return &MockLLMClient{
		responses: make(map[string]string),
	}
}

func (m *MockLLMClient) SetResponse(key, response string) {
	m.responses[key] = response
}

func (m *MockLLMClient) Complete(ctx context.Context, prompt string) (string, error) {
	// Try to match prompt against configured keys
	for key, response := range m.responses {
		if strings.Contains(prompt, key) {
			return response, nil
		}
	}
	// Default response if no match
	return `{"entities": [], "relations": []}`, nil
}

func (m *MockLLMClient) CompleteWithJSON(ctx context.Context, prompt string) (map[string]interface{}, error) {
	return nil, nil
}

func TestKnowledgeGraph_AddEntity(t *testing.T) {
	kg := NewKnowledgeGraph()

	entity := &Entity{
		ID:          "e1",
		Name:        "Go Language",
		Type:        "Technology",
		Description: "A programming language",
	}

	kg.AddEntity(entity)

	retrieved, exists := kg.GetEntity("e1")
	if !exists {
		t.Fatal("Entity not found after adding")
	}

	if retrieved.Name != "Go Language" {
		t.Errorf("Expected name 'Go Language', got '%s'", retrieved.Name)
	}
}

func TestKnowledgeGraph_AddRelation(t *testing.T) {
	kg := NewKnowledgeGraph()

	// Add entities first
	kg.AddEntity(&Entity{ID: "e1", Name: "Go"})
	kg.AddEntity(&Entity{ID: "e2", Name: "Google"})

	relation := &Relation{
		ID:          "r1",
		Source:      "e1",
		Target:      "e2",
		Type:        "developed_by",
		Description: "Go was developed by Google",
	}

	err := kg.AddRelation(relation)
	if err != nil {
		t.Fatalf("Failed to add relation: %v", err)
	}

	retrieved, exists := kg.GetRelation("r1")
	if !exists {
		t.Fatal("Relation not found after adding")
	}

	if retrieved.Type != "developed_by" {
		t.Errorf("Expected type 'developed_by', got '%s'", retrieved.Type)
	}
}

func TestKnowledgeGraph_AddRelation_InvalidEntity(t *testing.T) {
	kg := NewKnowledgeGraph()

	relation := &Relation{
		ID:     "r1",
		Source: "nonexistent",
		Target: "e2",
		Type:   "test",
	}

	err := kg.AddRelation(relation)
	if err == nil {
		t.Error("Expected error when adding relation with nonexistent source entity")
	}
}

func TestKnowledgeGraph_FindEntitiesByName(t *testing.T) {
	kg := NewKnowledgeGraph()

	kg.AddEntity(&Entity{ID: "e1", Name: "Go Language"})
	kg.AddEntity(&Entity{ID: "e2", Name: "Python Language"})
	kg.AddEntity(&Entity{ID: "e3", Name: "Go Programming"})

	results := kg.FindEntitiesByName("go")
	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}
}

func TestKnowledgeGraph_GetOutgoingRelations(t *testing.T) {
	kg := NewKnowledgeGraph()

	kg.AddEntity(&Entity{ID: "e1", Name: "Go"})
	kg.AddEntity(&Entity{ID: "e2", Name: "Google"})
	kg.AddEntity(&Entity{ID: "e3", Name: "Cloud"})

	kg.AddRelation(&Relation{ID: "r1", Source: "e1", Target: "e2", Type: "developed_by"})
	kg.AddRelation(&Relation{ID: "r2", Source: "e1", Target: "e3", Type: "used_in"})

	outgoing := kg.GetOutgoingRelations("e1")
	if len(outgoing) != 2 {
		t.Errorf("Expected 2 outgoing relations, got %d", len(outgoing))
	}
}

func TestKnowledgeGraph_GetIncomingRelations(t *testing.T) {
	kg := NewKnowledgeGraph()

	kg.AddEntity(&Entity{ID: "e1", Name: "Go"})
	kg.AddEntity(&Entity{ID: "e2", Name: "Google"})
	kg.AddEntity(&Entity{ID: "e3", Name: "Python"})

	kg.AddRelation(&Relation{ID: "r1", Source: "e1", Target: "e2", Type: "developed_by"})
	kg.AddRelation(&Relation{ID: "r2", Source: "e3", Target: "e2", Type: "developed_by"})

	incoming := kg.GetIncomingRelations("e2")
	if len(incoming) != 2 {
		t.Errorf("Expected 2 incoming relations, got %d", len(incoming))
	}
}

func TestKnowledgeGraph_ExtractSubgraph(t *testing.T) {
	kg := NewKnowledgeGraph()

	// Build a simple graph: e1 -> e2 -> e3 -> e4
	kg.AddEntity(&Entity{ID: "e1", Name: "Entity1"})
	kg.AddEntity(&Entity{ID: "e2", Name: "Entity2"})
	kg.AddEntity(&Entity{ID: "e3", Name: "Entity3"})
	kg.AddEntity(&Entity{ID: "e4", Name: "Entity4"})

	kg.AddRelation(&Relation{ID: "r1", Source: "e1", Target: "e2", Type: "relates_to"})
	kg.AddRelation(&Relation{ID: "r2", Source: "e2", Target: "e3", Type: "relates_to"})
	kg.AddRelation(&Relation{ID: "r3", Source: "e3", Target: "e4", Type: "relates_to"})

	// Extract subgraph with 1 hop from e1
	subgraph := kg.ExtractSubgraph([]string{"e1"}, 1)

	// Should include e1, e2 and r1
	if len(subgraph.Entities) < 2 {
		t.Errorf("Expected at least 2 entities in 1-hop subgraph, got %d", len(subgraph.Entities))
	}

	// Extract subgraph with 2 hops from e1
	subgraph = kg.ExtractSubgraph([]string{"e1"}, 2)

	// Should include e1, e2, e3 and r1, r2
	if len(subgraph.Entities) < 3 {
		t.Errorf("Expected at least 3 entities in 2-hop subgraph, got %d", len(subgraph.Entities))
	}
}

func TestKnowledgeGraphBuilder_ExtractFromText(t *testing.T) {
	mockLLM := NewMockLLMClient()
	mockLLM.SetResponse("test", `{
		"entities": [
			{
				"id": "doc_test_entity_1",
				"name": "Go",
				"type": "Technology",
				"description": "Programming language"
			},
			{
				"id": "doc_test_entity_2",
				"name": "Google",
				"type": "Organization",
				"description": "Tech company"
			}
		],
		"relations": [
			{
				"id": "doc_test_rel_1",
				"source": "doc_test_entity_1",
				"target": "doc_test_entity_2",
				"type": "developed_by",
				"description": "Go was developed by Google"
			}
		]
	}`)

	builder := NewKnowledgeGraphBuilder(mockLLM)

	ctx := context.Background()
	err := builder.extractFromText(ctx, "Go is a programming language developed by Google", "test")
	if err != nil {
		t.Fatalf("Failed to extract from text: %v", err)
	}

	graph := builder.GetGraph()

	// Check entities were added
	entity, exists := graph.GetEntity("doc_test_entity_1")
	if !exists {
		t.Error("Expected entity doc_test_entity_1 to be added")
	}
	if entity != nil && entity.Name != "Go" {
		t.Errorf("Expected entity name 'Go', got '%s'", entity.Name)
	}

	// Check relation was added
	relation, exists := graph.GetRelation("doc_test_rel_1")
	if !exists {
		t.Error("Expected relation doc_test_rel_1 to be added")
	}
	if relation != nil && relation.Type != "developed_by" {
		t.Errorf("Expected relation type 'developed_by', got '%s'", relation.Type)
	}
}

func TestKnowledgeGraphBuilder_BuildFromChunks(t *testing.T) {
	mockLLM := NewMockLLMClient()
	// Use a key that will be found in the prompt
	mockLLM.SetResponse("Extract entities and relations", `{
		"entities": [
			{
				"id": "doc_1_entity_1",
				"name": "RAG",
				"type": "Concept",
				"description": "Retrieval Augmented Generation"
			}
		],
		"relations": []
	}`)

	builder := NewKnowledgeGraphBuilder(mockLLM)

	chunks := []storage.Chunk{
		{
			ID:         "chunk1",
			DocumentID: "doc1",
			Text:       "RAG is a technique for improving language model outputs",
		},
	}

	ctx := context.Background()
	err := builder.BuildFromChunks(ctx, chunks)
	if err != nil {
		t.Fatalf("Failed to build from chunks: %v", err)
	}

	graph := builder.GetGraph()
	entity, exists := graph.GetEntity("doc_1_entity_1")
	if !exists {
		t.Error("Expected entity to be extracted from chunk")
	}
	if entity != nil && entity.Name != "RAG" {
		t.Errorf("Expected entity name 'RAG', got '%s'", entity.Name)
	}
}

func TestGraphRetriever_Search(t *testing.T) {
	mockLLM := NewMockLLMClient()
	// Use a key that will match in the prompt
	mockLLM.SetResponse("Extract the key entities", `["Go", "Google"]`)

	// Build a test graph
	kg := NewKnowledgeGraph()
	kg.AddEntity(&Entity{ID: "e1", Name: "Go"})
	kg.AddEntity(&Entity{ID: "e2", Name: "Google"})
	kg.AddEntity(&Entity{ID: "e3", Name: "Cloud"})

	kg.AddRelation(&Relation{ID: "r1", Source: "e1", Target: "e2", Type: "developed_by"})
	kg.AddRelation(&Relation{ID: "r2", Source: "e2", Target: "e3", Type: "provides"})

	retriever := NewGraphRetriever(kg, mockLLM)

	ctx := context.Background()
	subgraph, err := retriever.Search(ctx, SearchOptions{
		Query:   "Tell me about Go and Google",
		MaxHops: 2,
	})

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(subgraph.Entities) == 0 {
		t.Error("Expected subgraph to contain entities")
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain JSON",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "markdown JSON block",
			input:    "```json\n{\"key\": \"value\"}\n```",
			expected: `{"key": "value"}`,
		},
		{
			name:     "generic markdown block",
			input:    "```\n{\"key\": \"value\"}\n```",
			expected: `{"key": "value"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractJSON(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}
