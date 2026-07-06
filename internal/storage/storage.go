package storage

import (
	"time"
)

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

// Storage defines the interface for storage implementations
type Storage interface {
	Close() error
	CreateDocument(doc *Document) error
	UpdateDocumentStatus(id, status, errMsg string) error
	GetDocument(id string) (*Document, error)
	ListDocuments(limit, offset int) ([]Document, error)
	DeleteDocument(id string) error
	CreateChunk(chunk *Chunk) error
	CreateChunks(chunks []Chunk) error
	GetChunksByDocument(docID string) ([]Chunk, error)
	GetAllChunks() ([]Chunk, error)
	SearchByKeyword(query string, limit int) ([]Chunk, error)
}

// NewStorage creates a new storage instance (defaults to SQLite)
func NewStorage(dbPath string) (Storage, error) {
	return NewSQLiteStorage(dbPath)
}
