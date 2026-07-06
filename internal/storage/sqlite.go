package storage

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// SQLiteStorage implements storage using SQLite database
type SQLiteStorage struct {
	db *sql.DB
}

// NewSQLiteStorage creates a new SQLite storage instance
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open database with WAL mode for better concurrency
	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_busy_timeout=5000&_foreign_keys=1")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection limits
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	// Create tables
	if err := createTables(db); err != nil {
		db.Close()
		return nil, err
	}

	return &SQLiteStorage{db: db}, nil
}

func createTables(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			file_path TEXT NOT NULL,
			doc_type TEXT NOT NULL,
			content_type TEXT,
			status TEXT NOT NULL,
			error TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_docs_status ON documents(status)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL,
			text TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			embedding BLOB,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (document_id) REFERENCES documents(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks(document_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_index ON chunks(document_id, chunk_index)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}
	return nil
}

// Close closes the database connection
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// CreateDocument creates a new document record
func (s *SQLiteStorage) CreateDocument(doc *Document) error {
	if doc.ID == "" {
		doc.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	doc.CreatedAt = now
	doc.UpdatedAt = now

	_, err := s.db.Exec(`
		INSERT INTO documents (id, name, file_path, doc_type, content_type, status, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.Name, doc.FilePath, doc.DocType, doc.ContentType, doc.Status, doc.Error, doc.CreatedAt, doc.UpdatedAt,
	)
	return err
}

// UpdateDocumentStatus updates document status
func (s *SQLiteStorage) UpdateDocumentStatus(id, status, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE documents SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, time.Now().UTC(), id,
	)
	return err
}

// GetDocument retrieves a document by ID
func (s *SQLiteStorage) GetDocument(id string) (*Document, error) {
	row := s.db.QueryRow(`
		SELECT id, name, file_path, doc_type, content_type, status, error, created_at, updated_at
		FROM documents WHERE id = ?`, id)

	var doc Document
	err := row.Scan(&doc.ID, &doc.Name, &doc.FilePath, &doc.DocType,
		&doc.ContentType, &doc.Status, &doc.Error, &doc.CreatedAt, &doc.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &doc, err
}

// ListDocuments lists all documents
func (s *SQLiteStorage) ListDocuments(limit, offset int) ([]Document, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.Query(`
		SELECT id, name, file_path, doc_type, content_type, status, error, created_at, updated_at
		FROM documents ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var doc Document
		err := rows.Scan(&doc.ID, &doc.Name, &doc.FilePath, &doc.DocType,
			&doc.ContentType, &doc.Status, &doc.Error, &doc.CreatedAt, &doc.UpdatedAt)
		if err != nil {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

// DeleteDocument deletes a document and its chunks
func (s *SQLiteStorage) DeleteDocument(id string) error {
	_, err := s.db.Exec(`DELETE FROM documents WHERE id = ?`, id)
	return err
}

// CreateChunk creates a single chunk (for async processing)
func (s *SQLiteStorage) CreateChunk(chunk *Chunk) error {
	if chunk.ID == "" {
		chunk.ID = uuid.NewString()
	}
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = time.Now().UTC()
	}

	var emb []byte
	if len(chunk.Embedding) > 0 {
		emb = float32sToBytes(chunk.Embedding)
	}

	_, err := s.db.Exec(`
		INSERT INTO chunks (id, document_id, text, chunk_index, embedding, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		chunk.ID, chunk.DocumentID, chunk.Text, chunk.Index, emb, chunk.CreatedAt,
	)
	return err
}

// CreateChunks creates multiple chunks in a transaction
func (s *SQLiteStorage) CreateChunks(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO chunks (id, document_id, text, chunk_index, embedding, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, chunk := range chunks {
		if chunk.ID == "" {
			chunk.ID = uuid.NewString()
		}

		var emb []byte
		if len(chunk.Embedding) > 0 {
			emb = float32sToBytes(chunk.Embedding)
		}

		if _, err := stmt.Exec(chunk.ID, chunk.DocumentID, chunk.Text, chunk.Index, emb, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetChunksByDocument retrieves all chunks for a document
func (s *SQLiteStorage) GetChunksByDocument(docID string) ([]Chunk, error) {
	rows, err := s.db.Query(`
		SELECT id, document_id, text, chunk_index, embedding, created_at
		FROM chunks WHERE document_id = ? ORDER BY chunk_index`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var chunk Chunk
		var emb []byte
		err := rows.Scan(&chunk.ID, &chunk.DocumentID, &chunk.Text, &chunk.Index, &emb, &chunk.CreatedAt)
		if err != nil {
			continue
		}
		if len(emb) > 0 {
			chunk.Embedding = bytesToFloat32s(emb)
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

// GetAllChunks retrieves all chunks with embeddings
func (s *SQLiteStorage) GetAllChunks() ([]Chunk, error) {
	rows, err := s.db.Query(`
		SELECT id, document_id, text, chunk_index, embedding, created_at
		FROM chunks WHERE embedding IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var chunk Chunk
		var emb []byte
		err := rows.Scan(&chunk.ID, &chunk.DocumentID, &chunk.Text, &chunk.Index, &emb, &chunk.CreatedAt)
		if err != nil {
			continue
		}
		if len(emb) > 0 {
			chunk.Embedding = bytesToFloat32s(emb)
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

// SearchByKeyword searches chunks using simple keyword matching
func (s *SQLiteStorage) SearchByKeyword(query string, limit int) ([]Chunk, error) {
	// Simple LIKE-based search
	likeQuery := "%" + query + "%"
	
	rows, err := s.db.Query(`
		SELECT id, document_id, text, chunk_index, embedding, created_at
		FROM chunks WHERE text LIKE ? LIMIT ?`, likeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var chunk Chunk
		var emb []byte
		err := rows.Scan(&chunk.ID, &chunk.DocumentID, &chunk.Text, &chunk.Index, &emb, &chunk.CreatedAt)
		if err != nil {
			continue
		}
		if len(emb) > 0 {
			chunk.Embedding = bytesToFloat32s(emb)
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

// Helper functions
func float32sToBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], uint32(f))
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
		v[i] = float32(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
