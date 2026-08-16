package storage

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// writeOpType identifies the kind of write operation to be executed serially.
type writeOpType int

const (
	opCreateDocument writeOpType = iota
	opUpdateDocumentStatus
	opDeleteDocument
	opCreateChunk
	opCreateChunks
	opCreateWikiIndex
	opDeleteWikiIndex
	opCreateWikiEntry
	opUpdateWikiEntry
	opDeleteWikiEntry
)

// writeOp represents a single database write request sent to the worker.
type writeOp struct {
	opType  writeOpType
	payload interface{}
	result  chan error
}

// SQLiteStorage implements storage using SQLite database.
// All mutating operations are funneled through a single worker goroutine via
// writeCh to avoid SQLITE_BUSY errors from concurrent writes.
type SQLiteStorage struct {
	db      *sql.DB
	writeCh chan writeOp
	done    chan struct{}
	wg      sync.WaitGroup
}

// NewSQLiteStorage creates a new SQLite storage instance
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open database with WAL mode for better concurrency
	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_busy_timeout=5000&_fk=1")
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

	s := &SQLiteStorage{
		db:      db,
		writeCh: make(chan writeOp, 64),
		done:    make(chan struct{}),
	}
	s.startWorker()
	return s, nil
}

// startWorker launches the single goroutine responsible for all writes.
func (s *SQLiteStorage) startWorker() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-s.done:
				return
			case op := <-s.writeCh:
				op.result <- s.execWriteOp(op)
			}
		}
	}()
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
		`CREATE TABLE IF NOT EXISTS wiki_indexes (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS wiki_entries (
			id TEXT PRIMARY KEY,
			index_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (index_id) REFERENCES wiki_indexes(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wiki_entries_index ON wiki_entries(index_id)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}
	return nil
}

// Close shuts down the write worker and closes the database connection.
func (s *SQLiteStorage) Close() error {
	close(s.done)
	// Drain any pending write operations so callers are not blocked forever.
	for {
		select {
		case op := <-s.writeCh:
			op.result <- s.execWriteOp(op)
		default:
			goto drained
		}
	}
drained:
	s.wg.Wait()
	return s.db.Close()
}

// sendWriteOp sends an operation to the worker and waits for its result.
// Returns an error if the storage has already been closed.
func (s *SQLiteStorage) sendWriteOp(op writeOp) error {
	result := make(chan error, 1)
	op.result = result
	select {
	case s.writeCh <- op:
		return <-result
	case <-s.done:
		return fmt.Errorf("storage closed")
	}
}

// execWriteOp performs the actual database write for a writeOp.
// It is always executed by the single worker goroutine.
func (s *SQLiteStorage) execWriteOp(op writeOp) error {
	switch op.opType {
	case opCreateDocument:
		doc := op.payload.(*Document)
		_, err := s.db.Exec(`
			INSERT INTO documents (id, name, file_path, doc_type, content_type, status, error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			doc.ID, doc.Name, doc.FilePath, doc.DocType, doc.ContentType, doc.Status, doc.Error, doc.CreatedAt, doc.UpdatedAt,
		)
		return err

	case opUpdateDocumentStatus:
		p := op.payload.(struct {
			id      string
			status  string
			errMsg  string
			updated time.Time
		})
		_, err := s.db.Exec(`
			UPDATE documents SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
			p.status, p.errMsg, p.updated, p.id,
		)
		return err

	case opDeleteDocument:
		id := op.payload.(string)
		_, err := s.db.Exec(`DELETE FROM documents WHERE id = ?`, id)
		return err

	case opCreateChunk:
		chunk := op.payload.(*Chunk)
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

	case opCreateChunks:
		chunks := op.payload.([]Chunk)
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

	case opCreateWikiIndex:
		idx := op.payload.(*WikiIndex)
		_, err := s.db.Exec(`
			INSERT INTO wiki_indexes (id, title, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`,
			idx.ID, idx.Title, idx.Description, idx.CreatedAt, idx.UpdatedAt,
		)
		return err

	case opDeleteWikiIndex:
		id := op.payload.(string)
		// Delete entries first, then the index, within a single transaction.
		// This avoids relying on per-connection foreign key pragma settings.
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DELETE FROM wiki_entries WHERE index_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM wiki_indexes WHERE id = ?`, id); err != nil {
			return err
		}
		return tx.Commit()

	case opCreateWikiEntry:
		entry := op.payload.(*WikiEntry)
		_, err := s.db.Exec(`
			INSERT INTO wiki_entries (id, index_id, title, body, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			entry.ID, entry.IndexID, entry.Title, entry.Body, entry.CreatedAt, entry.UpdatedAt,
		)
		return err

	case opUpdateWikiEntry:
		entry := op.payload.(*WikiEntry)
		_, err := s.db.Exec(`
			UPDATE wiki_entries SET title = ?, body = ?, updated_at = ? WHERE id = ?`,
			entry.Title, entry.Body, entry.UpdatedAt, entry.ID,
		)
		return err

	case opDeleteWikiEntry:
		id := op.payload.(string)
		_, err := s.db.Exec(`DELETE FROM wiki_entries WHERE id = ?`, id)
		return err

	default:
		return fmt.Errorf("unknown write operation: %d", op.opType)
	}
}

// CreateDocument creates a new document record.
func (s *SQLiteStorage) CreateDocument(doc *Document) error {
	if doc.ID == "" {
		doc.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	doc.CreatedAt = now
	doc.UpdatedAt = now

	return s.sendWriteOp(writeOp{opType: opCreateDocument, payload: doc})
}

// UpdateDocumentStatus updates document status.
func (s *SQLiteStorage) UpdateDocumentStatus(id, status, errMsg string) error {
	payload := struct {
		id      string
		status  string
		errMsg  string
		updated time.Time
	}{id: id, status: status, errMsg: errMsg, updated: time.Now().UTC()}
	return s.sendWriteOp(writeOp{opType: opUpdateDocumentStatus, payload: payload})
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

// DeleteDocument deletes a document and its chunks.
func (s *SQLiteStorage) DeleteDocument(id string) error {
	return s.sendWriteOp(writeOp{opType: opDeleteDocument, payload: id})
}

// CreateChunk creates a single chunk (for async processing).
func (s *SQLiteStorage) CreateChunk(chunk *Chunk) error {
	if chunk.ID == "" {
		chunk.ID = uuid.NewString()
	}
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = time.Now().UTC()
	}
	return s.sendWriteOp(writeOp{opType: opCreateChunk, payload: chunk})
}

// CreateChunks creates multiple chunks in a transaction.
func (s *SQLiteStorage) CreateChunks(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return s.sendWriteOp(writeOp{opType: opCreateChunks, payload: chunks})
}

// GetChunkByIndex retrieves a single chunk by document ID and chunk index.
func (s *SQLiteStorage) GetChunkByIndex(docID string, index int) (*Chunk, error) {
	row := s.db.QueryRow(`
		SELECT id, document_id, text, chunk_index, embedding, created_at
		FROM chunks WHERE document_id = ? AND chunk_index = ?`, docID, index)

	var chunk Chunk
	var emb []byte
	err := row.Scan(&chunk.ID, &chunk.DocumentID, &chunk.Text, &chunk.Index, &emb, &chunk.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(emb) > 0 {
		chunk.Embedding = bytesToFloat32s(emb)
	}
	return &chunk, nil
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

// ---- Wiki index methods --------------------------------------------------

// CreateWikiIndex creates a new wiki index.
func (s *SQLiteStorage) CreateWikiIndex(idx *WikiIndex) error {
	if idx.ID == "" {
		idx.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	idx.CreatedAt = now
	idx.UpdatedAt = now
	return s.sendWriteOp(writeOp{opType: opCreateWikiIndex, payload: idx})
}

// GetWikiIndex retrieves a wiki index by ID.
func (s *SQLiteStorage) GetWikiIndex(id string) (*WikiIndex, error) {
	row := s.db.QueryRow(`
		SELECT id, title, description, created_at, updated_at
		FROM wiki_indexes WHERE id = ?`, id)

	var idx WikiIndex
	err := row.Scan(&idx.ID, &idx.Title, &idx.Description, &idx.CreatedAt, &idx.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &idx, err
}

// ListWikiIndexes lists all wiki indexes ordered by title.
func (s *SQLiteStorage) ListWikiIndexes() ([]WikiIndex, error) {
	rows, err := s.db.Query(`
		SELECT id, title, description, created_at, updated_at
		FROM wiki_indexes ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexes []WikiIndex
	for rows.Next() {
		var idx WikiIndex
		if err := rows.Scan(&idx.ID, &idx.Title, &idx.Description, &idx.CreatedAt, &idx.UpdatedAt); err != nil {
			continue
		}
		indexes = append(indexes, idx)
	}
	return indexes, rows.Err()
}

// DeleteWikiIndex deletes a wiki index and all its entries.
func (s *SQLiteStorage) DeleteWikiIndex(id string) error {
	return s.sendWriteOp(writeOp{opType: opDeleteWikiIndex, payload: id})
}

// ---- Wiki entry methods --------------------------------------------------

// CreateWikiEntry creates a new wiki entry.
func (s *SQLiteStorage) CreateWikiEntry(entry *WikiEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	entry.CreatedAt = now
	entry.UpdatedAt = now
	return s.sendWriteOp(writeOp{opType: opCreateWikiEntry, payload: entry})
}

// GetWikiEntry retrieves a wiki entry by ID.
func (s *SQLiteStorage) GetWikiEntry(id string) (*WikiEntry, error) {
	row := s.db.QueryRow(`
		SELECT id, index_id, title, body, created_at, updated_at
		FROM wiki_entries WHERE id = ?`, id)

	var entry WikiEntry
	err := row.Scan(&entry.ID, &entry.IndexID, &entry.Title, &entry.Body, &entry.CreatedAt, &entry.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &entry, err
}

// ListWikiEntries lists all wiki entries under an index.
func (s *SQLiteStorage) ListWikiEntries(indexID string) ([]WikiEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, index_id, title, body, created_at, updated_at
		FROM wiki_entries WHERE index_id = ? ORDER BY created_at DESC`, indexID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []WikiEntry
	for rows.Next() {
		var entry WikiEntry
		if err := rows.Scan(&entry.ID, &entry.IndexID, &entry.Title, &entry.Body, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// UpdateWikiEntry updates a wiki entry's title and body.
func (s *SQLiteStorage) UpdateWikiEntry(entry *WikiEntry) error {
	if entry.ID == "" {
		return fmt.Errorf("entry id is required")
	}
	entry.UpdatedAt = time.Now().UTC()
	return s.sendWriteOp(writeOp{opType: opUpdateWikiEntry, payload: entry})
}

// DeleteWikiEntry deletes a wiki entry.
func (s *SQLiteStorage) DeleteWikiEntry(id string) error {
	return s.sendWriteOp(writeOp{opType: opDeleteWikiEntry, payload: id})
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
