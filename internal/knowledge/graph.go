package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/user/go-rag/internal/llm"
	"github.com/user/go-rag/internal/storage"
)

// Entity represents an entity in the knowledge graph.
type Entity struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Properties  map[string]string `json:"properties"`
}

// Relation represents a relationship between two entities.
type Relation struct {
	ID          string            `json:"id"`
	Source      string            `json:"source"`       // Source entity ID
	Target      string            `json:"target"`       // Target entity ID
	Type        string            `json:"type"`         // Relation type
	Description string            `json:"description"`
	Properties  map[string]string `json:"properties"`
}

// KnowledgeGraph stores entities and relations.
type KnowledgeGraph struct {
	mu        sync.RWMutex
	entities  map[string]*Entity
	relations map[string]*Relation
	// Adjacency list for fast traversal: entityID -> []relationID
	outgoing map[string][]string
	incoming map[string][]string
}

// NewKnowledgeGraph creates a new empty knowledge graph.
func NewKnowledgeGraph() *KnowledgeGraph {
	return &KnowledgeGraph{
		entities:  make(map[string]*Entity),
		relations: make(map[string]*Relation),
		outgoing:  make(map[string][]string),
		incoming:  make(map[string][]string),
	}
}

// AddEntity adds or updates an entity in the graph.
func (kg *KnowledgeGraph) AddEntity(entity *Entity) {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	kg.entities[entity.ID] = entity
	if _, exists := kg.outgoing[entity.ID]; !exists {
		kg.outgoing[entity.ID] = []string{}
	}
	if _, exists := kg.incoming[entity.ID]; !exists {
		kg.incoming[entity.ID] = []string{}
	}
}

// AddRelation adds a relation to the graph.
func (kg *KnowledgeGraph) AddRelation(relation *Relation) error {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	// Verify entities exist
	if _, exists := kg.entities[relation.Source]; !exists {
		return fmt.Errorf("source entity %s not found", relation.Source)
	}
	if _, exists := kg.entities[relation.Target]; !exists {
		return fmt.Errorf("target entity %s not found", relation.Target)
	}

	kg.relations[relation.ID] = relation
	kg.outgoing[relation.Source] = append(kg.outgoing[relation.Source], relation.ID)
	kg.incoming[relation.Target] = append(kg.incoming[relation.Target], relation.ID)

	return nil
}

// GetEntity retrieves an entity by ID.
func (kg *KnowledgeGraph) GetEntity(id string) (*Entity, bool) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	entity, exists := kg.entities[id]
	return entity, exists
}

// GetRelation retrieves a relation by ID.
func (kg *KnowledgeGraph) GetRelation(id string) (*Relation, bool) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	relation, exists := kg.relations[id]
	return relation, exists
}

// FindEntitiesByName finds entities that match the given name (case-insensitive).
func (kg *KnowledgeGraph) FindEntitiesByName(name string) []*Entity {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	var results []*Entity
	lowerName := strings.ToLower(name)
	for _, entity := range kg.entities {
		if strings.Contains(strings.ToLower(entity.Name), lowerName) {
			results = append(results, entity)
		}
	}
	return results
}

// GetOutgoingRelations returns all outgoing relations for an entity.
func (kg *KnowledgeGraph) GetOutgoingRelations(entityID string) []*Relation {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	var results []*Relation
	for _, relID := range kg.outgoing[entityID] {
		if rel, exists := kg.relations[relID]; exists {
			results = append(results, rel)
		}
	}
	return results
}

// GetIncomingRelations returns all incoming relations for an entity.
func (kg *KnowledgeGraph) GetIncomingRelations(entityID string) []*Relation {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	var results []*Relation
	for _, relID := range kg.incoming[entityID] {
		if rel, exists := kg.relations[relID]; exists {
			results = append(results, rel)
		}
	}
	return results
}

// Subgraph represents a subgraph extracted from the knowledge graph.
type Subgraph struct {
	Entities  []*Entity
	Relations []*Relation
}

// ExtractSubgraph extracts a subgraph starting from seed entities with max hops.
func (kg *KnowledgeGraph) ExtractSubgraph(seedEntityIDs []string, maxHops int) *Subgraph {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	visited := make(map[string]bool)
	entitySet := make(map[string]*Entity)
	relationSet := make(map[string]*Relation)

	// BFS traversal
	queue := make([]struct {
		entityID string
		hops     int
	}, 0)

	for _, id := range seedEntityIDs {
		if entity, exists := kg.entities[id]; exists {
			queue = append(queue, struct {
				entityID string
				hops     int
			}{id, 0})
			entitySet[id] = entity
		}
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if visited[current.entityID] || current.hops > maxHops {
			continue
		}
		visited[current.entityID] = true

		// Add outgoing relations
		for _, relID := range kg.outgoing[current.entityID] {
			rel := kg.relations[relID]
			relationSet[relID] = rel

			if !visited[rel.Target] && current.hops < maxHops {
				if target, exists := kg.entities[rel.Target]; exists {
					entitySet[rel.Target] = target
					queue = append(queue, struct {
						entityID string
						hops     int
					}{rel.Target, current.hops + 1})
				}
			}
		}

		// Add incoming relations
		for _, relID := range kg.incoming[current.entityID] {
			rel := kg.relations[relID]
			relationSet[relID] = rel

			if !visited[rel.Source] && current.hops < maxHops {
				if source, exists := kg.entities[rel.Source]; exists {
					entitySet[rel.Source] = source
					queue = append(queue, struct {
						entityID string
						hops     int
					}{rel.Source, current.hops + 1})
				}
			}
		}
	}

	// Convert sets to slices
	entities := make([]*Entity, 0, len(entitySet))
	for _, e := range entitySet {
		entities = append(entities, e)
	}

	relations := make([]*Relation, 0, len(relationSet))
	for _, r := range relationSet {
		relations = append(relations, r)
	}

	return &Subgraph{
		Entities:  entities,
		Relations: relations,
	}
}

// KnowledgeGraphBuilder builds knowledge graphs from documents using LLM.
type KnowledgeGraphBuilder struct {
	llmClient llm.Client
	graph     *KnowledgeGraph
}

// NewKnowledgeGraphBuilder creates a new knowledge graph builder.
func NewKnowledgeGraphBuilder(llmClient llm.Client) *KnowledgeGraphBuilder {
	return &KnowledgeGraphBuilder{
		llmClient: llmClient,
		graph:     NewKnowledgeGraph(),
	}
}

// GetGraph returns the built knowledge graph.
func (kgb *KnowledgeGraphBuilder) GetGraph() *KnowledgeGraph {
	return kgb.graph
}

// ExtractedData represents entities and relations extracted from text.
type ExtractedData struct {
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations"`
}

// BuildFromChunks builds a knowledge graph from document chunks.
func (kgb *KnowledgeGraphBuilder) BuildFromChunks(ctx context.Context, chunks []storage.Chunk) error {
	for _, chunk := range chunks {
		if err := kgb.extractFromText(ctx, chunk.Text, chunk.DocumentID); err != nil {
			// Log error but continue processing other chunks
			fmt.Printf("Warning: failed to extract from chunk %s: %v\n", chunk.ID, err)
			continue
		}
	}
	return nil
}

// extractFromText extracts entities and relations from a text using LLM.
func (kgb *KnowledgeGraphBuilder) extractFromText(ctx context.Context, text, documentID string) error {
	prompt := fmt.Sprintf(`You are a knowledge graph extraction system. Extract entities and relations from the following text.

Text:
%s

Return a JSON object with the following structure:
{
  "entities": [
    {
      "id": "unique_id",
      "name": "entity name",
      "type": "entity type (person, organization, concept, etc.)",
      "description": "brief description"
    }
  ],
  "relations": [
    {
      "id": "unique_id",
      "source": "source_entity_id",
      "target": "target_entity_id",
      "type": "relation type (is_a, part_of, relates_to, etc.)",
      "description": "brief description"
    }
  ]
}

Guidelines:
- Extract key entities (people, organizations, concepts, locations, etc.)
- Identify meaningful relationships between entities
- Use descriptive relation types
- Keep entity IDs consistent within the response
- Use format: doc_%s_entity_N for entity IDs and doc_%s_rel_N for relation IDs`, text, documentID, documentID)

	response, err := kgb.llmClient.Complete(ctx, prompt)
	if err != nil {
		return fmt.Errorf("LLM extraction failed: %w", err)
	}

	// Try to parse JSON from response (may be wrapped in markdown code blocks)
	jsonStr := extractJSON(response)

	var extracted ExtractedData
	if err := json.Unmarshal([]byte(jsonStr), &extracted); err != nil {
		return fmt.Errorf("failed to parse extraction result: %w", err)
	}

	// Add entities to graph
	for i := range extracted.Entities {
		entity := &extracted.Entities[i]
		if entity.Properties == nil {
			entity.Properties = make(map[string]string)
		}
		entity.Properties["document_id"] = documentID
		kgb.graph.AddEntity(entity)
	}

	// Add relations to graph
	for i := range extracted.Relations {
		relation := &extracted.Relations[i]
		if relation.Properties == nil {
			relation.Properties = make(map[string]string)
		}
		relation.Properties["document_id"] = documentID
		if err := kgb.graph.AddRelation(relation); err != nil {
			// Skip invalid relations
			fmt.Printf("Warning: skipping invalid relation: %v\n", err)
		}
	}

	return nil
}

// extractJSON extracts JSON from a string that may be wrapped in markdown code blocks.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)

	// Remove markdown code blocks if present
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSpace(s)
	}

	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}

	return s
}

// GraphRetriever performs graph-based retrieval.
type GraphRetriever struct {
	graph     *KnowledgeGraph
	llmClient llm.Client
}

// NewGraphRetriever creates a new graph-based retriever.
func NewGraphRetriever(graph *KnowledgeGraph, llmClient llm.Client) *GraphRetriever {
	return &GraphRetriever{
		graph:     graph,
		llmClient: llmClient,
	}
}

// SearchOptions contains search parameters for graph retrieval.
type SearchOptions struct {
	Query   string
	MaxHops int
	TopK    int
}

// Search performs graph-based retrieval for a query.
func (gr *GraphRetriever) Search(ctx context.Context, opts SearchOptions) (*Subgraph, error) {
	// Link entities in query to graph
	entityIDs, err := gr.linkEntities(ctx, opts.Query)
	if err != nil {
		return nil, fmt.Errorf("entity linking failed: %w", err)
	}

	if len(entityIDs) == 0 {
		return &Subgraph{}, nil
	}

	// Extract subgraph with multi-hop traversal
	maxHops := opts.MaxHops
	if maxHops <= 0 {
		maxHops = 2
	}

	subgraph := gr.graph.ExtractSubgraph(entityIDs, maxHops)
	return subgraph, nil
}

// linkEntities links entities mentioned in the query to entities in the graph.
func (gr *GraphRetriever) linkEntities(ctx context.Context, query string) ([]string, error) {
	prompt := fmt.Sprintf(`Extract the key entities mentioned in the following query:

Query: %s

Return ONLY a JSON array of entity names, for example: ["entity1", "entity2"]`, query)

	response, err := gr.llmClient.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM entity extraction failed: %w", err)
	}

	jsonStr := extractJSON(response)

	var entityNames []string
	if err := json.Unmarshal([]byte(jsonStr), &entityNames); err != nil {
		return nil, fmt.Errorf("failed to parse entity names: %w", err)
	}

	// Find matching entities in graph
	var entityIDs []string
	for _, name := range entityNames {
		entities := gr.graph.FindEntitiesByName(name)
		for _, entity := range entities {
			entityIDs = append(entityIDs, entity.ID)
		}
	}

	return entityIDs, nil
}
