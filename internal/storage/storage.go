package storage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/google/uuid"
)

// Storage handles all document storage operations using JSON files.
type Storage struct {
	basePath   string
	docsPath   string
	chunksPath string
}

// Document represents a document in the storage.
type Document struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	FilePath    string    `json:"file_path"`
	DocType     string    `json:"doc_type"`
	ContentType string    `json:"content_type"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Chunk represents a text chunk in the storage.
type Chunk struct {
	ID         string    `json:"id"`
	DocumentID string    `json:"document_id"`
	Text       string    `json:"text"`
	Index      int       `json:"index"`
	Embedding  []float32 `json:"embedding,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// SearchResult represents a search result.
type SearchResult struct {
	Chunk Chunk   `json:"chunk"`
	Score float64 `json:"score"`
}

// NewStorage creates a new Storage instance.
func NewStorage(basePath string) (*Storage, error) {
	// Ensure directory exists
	docsPath := filepath.Join(basePath, "documents")
	chunksPath := filepath.Join(basePath, "chunks")

	for _, dir := range []string{docsPath, chunksPath} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return &Storage{
		basePath:   basePath,
		docsPath:   docsPath,
		chunksPath: chunksPath,
	}, nil
}

// Close is a no-op for file-based storage.
func (s *Storage) Close() error {
	return nil
}

// CreateDocument creates a new document record.
func (s *Storage) CreateDocument(doc *Document) error {
	if doc.ID == "" {
		doc.ID = uuid.NewString()
	}

	now := time.Now().UTC()
	doc.CreatedAt = now
	doc.UpdatedAt = now

	filePath := filepath.Join(s.docsPath, doc.ID+".json")
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal document: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write document: %w", err)
	}

	return nil
}

// UpdateDocumentStatus updates the status of a document.
func (s *Storage) UpdateDocumentStatus(id, status, errMsg string) error {
	doc, err := s.GetDocument(id)
	if err != nil {
		return err
	}
	if doc == nil {
		return fmt.Errorf("document not found: %s", id)
	}

	doc.Status = status
	doc.Error = errMsg
	doc.UpdatedAt = time.Now().UTC()

	return s.CreateDocument(doc)
}

// GetDocument retrieves a document by ID.
func (s *Storage) GetDocument(id string) (*Document, error) {
	filePath := filepath.Join(s.docsPath, id+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	return &doc, nil
}

// ListDocuments lists all documents.
func (s *Storage) ListDocuments(limit, offset int) ([]Document, error) {
	entries, err := os.ReadDir(s.docsPath)
	if err != nil {
		return nil, err
	}

	var docs []Document
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), ".json")
		doc, err := s.GetDocument(id)
		if err != nil {
			continue
		}
		if doc != nil {
			docs = append(docs, *doc)
		}
	}

	// Sort by creation time (newest first)
	for i := 0; i < len(docs)-1; i++ {
		for j := i + 1; j < len(docs); j++ {
			if docs[j].CreatedAt.After(docs[i].CreatedAt) {
				docs[i], docs[j] = docs[j], docs[i]
			}
		}
	}

	// Apply offset and limit
	if offset >= len(docs) {
		return []Document{}, nil
	}

	docs = docs[offset:]
	if limit > 0 && limit < len(docs) {
		docs = docs[:limit]
	}

	return docs, nil
}

// DeleteDocument deletes a document and its chunks.
func (s *Storage) DeleteDocument(id string) error {
	// Delete document file
	docPath := filepath.Join(s.docsPath, id+".json")
	os.Remove(docPath)

	// Delete all chunks for this document
	chunks, _ := s.GetChunksByDocument(id)
	for _, chunk := range chunks {
		chunkPath := filepath.Join(s.chunksPath, chunk.ID+".json")
		os.Remove(chunkPath)
	}

	return nil
}

// CreateChunks creates multiple chunks for a document.
func (s *Storage) CreateChunks(chunks []Chunk) error {
	for _, chunk := range chunks {
		if chunk.ID == "" {
			chunk.ID = uuid.NewString()
		}
		if chunk.CreatedAt.IsZero() {
			chunk.CreatedAt = time.Now().UTC()
		}

		filePath := filepath.Join(s.chunksPath, chunk.ID+".json")
		data, err := json.MarshalIndent(chunk, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal chunk: %w", err)
		}

		if err := os.WriteFile(filePath, data, 0644); err != nil {
			return fmt.Errorf("failed to write chunk: %w", err)
		}
	}

	return nil
}

// GetChunksByDocument retrieves all chunks for a document.
func (s *Storage) GetChunksByDocument(docID string) ([]Chunk, error) {
	entries, err := os.ReadDir(s.chunksPath)
	if err != nil {
		return nil, err
	}

	var chunks []Chunk
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(s.chunksPath, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var chunk Chunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}

		if chunk.DocumentID == docID {
			chunks = append(chunks, chunk)
		}
	}

	// Sort by index
	for i := 0; i < len(chunks)-1; i++ {
		for j := i + 1; j < len(chunks); j++ {
			if chunks[j].Index < chunks[i].Index {
				chunks[i], chunks[j] = chunks[j], chunks[i]
			}
		}
	}

	return chunks, nil
}

// GetAllChunks retrieves all chunks with embeddings.
func (s *Storage) GetAllChunks() ([]Chunk, error) {
	entries, err := os.ReadDir(s.chunksPath)
	if err != nil {
		return nil, err
	}

	var chunks []Chunk
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(s.chunksPath, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var chunk Chunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}

		if len(chunk.Embedding) > 0 {
			chunks = append(chunks, chunk)
		}
	}

	return chunks, nil
}

// SearchByKeyword searches chunks using simple keyword matching.
func (s *Storage) SearchByKeyword(query string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 20
	}

	chunks, err := s.GetAllChunks()
	if err != nil {
		return nil, err
	}

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	// Score chunks based on keyword matches
	type scoredChunk struct {
		chunk Chunk
		score int
	}

	var scored []scoredChunk
	for _, chunk := range chunks {
		textLower := strings.ToLower(chunk.Text)
		score := 0

		// Check for full query match
		if strings.Contains(textLower, queryLower) {
			score += 10
		}

		// Check for word matches
		for _, word := range queryWords {
			if strings.Contains(textLower, word) {
				score += 1
			}
		}

		if score > 0 {
			scored = append(scored, scoredChunk{chunk: chunk, score: score})
		}
	}

	// Sort by score
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Take top results
	if len(scored) > limit {
		scored = scored[:limit]
	}

	result := make([]Chunk, len(scored))
	for i, sc := range scored {
		result[i] = sc.chunk
	}

	return result, nil
}

// Helper functions for float32 serialization

func float32sToBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], float32ToBits(f))
	}
	return buf
}

func bytesToFloat32s(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = bitsToFloat32(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// float32ToBits returns the IEEE 754 binary representation of f.
func float32ToBits(f float32) uint32 {
	return *(*uint32)(unsafe.Pointer(&f))
}

// bitsToFloat32 returns the float32 value corresponding to the IEEE 754 binary representation b.
func bitsToFloat32(b uint32) float32 {
	return *(*float32)(unsafe.Pointer(&b))
}
