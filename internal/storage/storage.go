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

// DocumentQuery describes how documents are selected when listing.
type DocumentQuery struct {
	// Search is a case-insensitive substring matched against the document
	// name and file path. Empty disables text matching.
	Search string
	// Filters are exact-match column filters. Multiple values for the same
	// key are combined with SQL IN. Supported keys:
	//   "status", "type" (doc_type), "name", "path" (file_path).
	Filters map[string][]string
	// Limit is the maximum number of documents returned. 0 means no limit.
	Limit int
	// Offset is the number of matching documents to skip.
	Offset int
}

// WikiIndex represents a topic/category index in the personal wiki.
type WikiIndex struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WikiEntry represents a single wiki item under a WikiIndex.
type WikiEntry struct {
	ID        string    `json:"id"`
	IndexID   string    `json:"index_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Storage defines the interface for storage implementations
type Storage interface {
	Close() error
	CreateDocument(doc *Document) error
	UpdateDocumentStatus(id, status, errMsg string) error
	GetDocument(id string) (*Document, error)
	ListDocuments(query DocumentQuery) ([]Document, error)
	CountDocuments(query DocumentQuery) (int, error)
	// DeleteDocument removes a document and all of its chunks in a single
	// transaction. It returns the number of chunks that were removed together
	// with the document.
	DeleteDocument(id string) (int64, error)
	CreateChunk(chunk *Chunk) error
	CreateChunks(chunks []Chunk) error
	GetChunkByIndex(docID string, index int) (*Chunk, error)
	GetChunksByDocument(docID string) ([]Chunk, error)
	GetAllChunks() ([]Chunk, error)
	SearchByKeyword(query string, limit int) ([]Chunk, error)

	// Orphan chunk maintenance. A chunk is an orphan when its document no
	// longer exists — data left behind by deletes issued before cascade
	// deletes were reliable.
	CountOrphanChunks() (int, error)
	DeleteOrphanChunks() (int64, error)

	// Wiki index management.
	CreateWikiIndex(idx *WikiIndex) error
	GetWikiIndex(id string) (*WikiIndex, error)
	ListWikiIndexes() ([]WikiIndex, error)
	DeleteWikiIndex(id string) error

	// Wiki entry management.
	CreateWikiEntry(entry *WikiEntry) error
	GetWikiEntry(id string) (*WikiEntry, error)
	ListWikiEntries(indexID string) ([]WikiEntry, error)
	UpdateWikiEntry(entry *WikiEntry) error
	DeleteWikiEntry(id string) error
}

// NewStorage creates a new storage instance (defaults to SQLite)
func NewStorage(dbPath string) (Storage, error) {
	return NewSQLiteStorage(dbPath)
}
