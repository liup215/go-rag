package storage

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Lock-wait constants for the outer retry loop around multi-statement writes.
// The driver's busy_timeout pragma already waits inside SQLite for the lock;
// the retry covers the residual cases a busy timeout cannot resolve, such as
// a COMMIT that could not upgrade to an exclusive lock.
const (
	busyRetryAttempts = 5
	busyRetryDelay    = 200 * time.Millisecond
)

// writeOpType identifies the kind of write operation to be executed serially.
type writeOpType int

const (
	opCreateDocument writeOpType = iota
	opUpdateDocumentStatus
	opDeleteDocument
	opDeleteOrphanChunks
	opCreateChunk
	opCreateChunks
	opCreateWikiIndex
	opDeleteWikiIndex
	opCreateWikiEntry
	opUpdateWikiEntry
	opDeleteWikiEntry
)

// writeOp represents a single database write request sent to the worker.
// Ops are passed by pointer so the worker can return extra results (such as
// the number of chunks removed by a cascade delete) to the waiting caller:
// reading the field is safe after receiving from op.result.
type writeOp struct {
	opType        writeOpType
	payload       interface{}
	result        chan error
	chunksDeleted int64
}

// SQLiteStorage implements storage using SQLite database.
// All mutating operations are funneled through a single worker goroutine via
// writeCh to avoid SQLITE_BUSY errors from concurrent writes.
type SQLiteStorage struct {
	db      *sql.DB
	writeCh chan *writeOp
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

	// Open database. modernc.org/sqlite only honours `?_pragma=<statement>`
	// query parameters; the mattn-style names used previously (`_journal`,
	// `_busy_timeout`, `_fk`) were silently ignored, so foreign keys — and with
	// them the schema's ON DELETE CASCADE — were never actually enabled, which
	// is what let deleted documents leave orphan chunks behind. The driver
	// pushes busy_timeout ahead of the other pragmas itself.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
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
		writeCh: make(chan *writeOp, 64),
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
func (s *SQLiteStorage) sendWriteOp(opType writeOpType, payload interface{}) error {
	op := &writeOp{opType: opType, payload: payload, result: make(chan error, 1)}
	select {
	case s.writeCh <- op:
		return <-op.result
	case <-s.done:
		return fmt.Errorf("storage closed")
	}
}

// sendWriteOpCount sends a write op to the worker and waits for its result,
// returning the number of rows the op removed (the chunks removed by a cascade
// document delete, or by an orphan-chunk cleanup).
func (s *SQLiteStorage) sendWriteOpCount(opType writeOpType, payload interface{}) (int64, error) {
	op := &writeOp{opType: opType, payload: payload, result: make(chan error, 1)}
	select {
	case s.writeCh <- op:
		err := <-op.result
		return op.chunksDeleted, err
	case <-s.done:
		return 0, fmt.Errorf("storage closed")
	}
}

// execWriteOp performs the actual database write for a writeOp.
// It is always executed by the single worker goroutine.
func (s *SQLiteStorage) execWriteOp(op *writeOp) error {
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

		n, err := s.deleteDocumentCascade(id)
		op.chunksDeleted = n
		return err

	case opDeleteOrphanChunks:
		n, err := s.deleteOrphanChunks()
		op.chunksDeleted = n
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
		// The whole batch is one transaction, so it is retried as a whole while
		// the database is locked: a failed attempt rolls back, meaning a retry
		// starts from scratch and cannot leave partial rows behind.
		return withBusyRetry(func() error {
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
		})

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
		// The transaction is retried as a whole while the database is locked: a
		// failed attempt rolls back, so a retry cannot leave a half-deleted
		// index behind.
		return withBusyRetry(func() error {
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
		})

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

	return s.sendWriteOp(opCreateDocument, doc)
}

// UpdateDocumentStatus updates document status.
func (s *SQLiteStorage) UpdateDocumentStatus(id, status, errMsg string) error {
	payload := struct {
		id      string
		status  string
		errMsg  string
		updated time.Time
	}{id: id, status: status, errMsg: errMsg, updated: time.Now().UTC()}
	return s.sendWriteOp(opUpdateDocumentStatus, payload)
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

// documentFilterColumns maps supported DocumentQuery filter keys to columns.
var documentFilterColumns = map[string]string{
	"status":    "status",
	"type":      "doc_type",
	"doc_type":  "doc_type",
	"name":      "name",
	"path":      "file_path",
	"file_path": "file_path",
}

// escapeLike escapes LIKE wildcard characters so search input is matched literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// buildDocumentWhere builds the WHERE clause and arguments for a DocumentQuery.
// Filter keys are applied in sorted order so the generated SQL is deterministic.
func buildDocumentWhere(q DocumentQuery) (string, []interface{}, error) {
	var conds []string
	var args []interface{}

	if search := strings.TrimSpace(q.Search); search != "" {
		// LIKE is case-insensitive for ASCII in SQLite by default.
		conds = append(conds, `(name LIKE ? ESCAPE '\' OR file_path LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(search) + "%"
		args = append(args, pattern, pattern)
	}

	keys := make([]string, 0, len(q.Filters))
	for key := range q.Filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		column, ok := documentFilterColumns[key]
		if !ok {
			return "", nil, fmt.Errorf("unsupported document filter %q (supported: status, type, name, path)", key)
		}

		values := q.Filters[key]
		if len(values) == 0 {
			continue
		}

		placeholders := make([]string, len(values))
		for i, value := range values {
			placeholders[i] = "?"
			args = append(args, value)
		}
		conds = append(conds, fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ", ")))
	}

	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// validateDocumentQuery rejects invalid pagination values.
func validateDocumentQuery(q DocumentQuery) error {
	if q.Limit < 0 {
		return fmt.Errorf("limit must be >= 0, got %d", q.Limit)
	}
	if q.Offset < 0 {
		return fmt.Errorf("offset must be >= 0, got %d", q.Offset)
	}
	return nil
}

// listDocumentSQL builds the SELECT statement for a DocumentQuery.
func listDocumentSQL(q DocumentQuery) (string, []interface{}, error) {
	if err := validateDocumentQuery(q); err != nil {
		return "", nil, err
	}

	where, args, err := buildDocumentWhere(q)
	if err != nil {
		return "", nil, err
	}

	query := `SELECT id, name, file_path, doc_type, content_type, status, error, created_at, updated_at
		FROM documents` + where + ` ORDER BY created_at DESC, id DESC`
	if q.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, q.Limit, q.Offset)
	} else if q.Offset > 0 {
		// LIMIT -1 returns all remaining rows in SQLite while still applying OFFSET.
		query += ` LIMIT -1 OFFSET ?`
		args = append(args, q.Offset)
	}
	return query, args, nil
}

// ListDocuments lists documents matching the query, newest first.
// The id tiebreaker keeps paging deterministic when documents share a timestamp.
func (s *SQLiteStorage) ListDocuments(query DocumentQuery) ([]Document, error) {
	sqlQuery, args, err := listDocumentSQL(query)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(sqlQuery, args...)
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

// CountDocuments returns the number of documents matching the query.
// Limit and Offset are ignored so callers can report a total.
func (s *SQLiteStorage) CountDocuments(query DocumentQuery) (int, error) {
	if err := validateDocumentQuery(query); err != nil {
		return 0, err
	}

	where, args, err := buildDocumentWhere(query)
	if err != nil {
		return 0, err
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM documents`+where, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// DeleteDocument deletes a document and all of its chunks in one transaction,
// returning the number of chunks that were removed.
func (s *SQLiteStorage) DeleteDocument(id string) (int64, error) {
	return s.sendWriteOpCount(opDeleteDocument, id)
}

// deleteDocumentCascade removes a document and all of its chunks in a single
// transaction and returns the number of chunks removed.
//
// The chunks table declares ON DELETE CASCADE, but SQLite only honours that on
// connections with PRAGMA foreign_keys enabled, so the chunks are deleted
// explicitly first — this also sweeps up rows that predate working foreign
// keys. Any failure rolls the whole transaction back, so a delete either
// removes the document together with every chunk, or leaves both untouched.
func (s *SQLiteStorage) deleteDocumentCascade(id string) (int64, error) {
	var chunksDeleted int64

	err := withBusyRetry(func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		res, err := tx.Exec(`DELETE FROM chunks WHERE document_id = ?`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}

		if _, err := tx.Exec(`DELETE FROM documents WHERE id = ?`, id); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		chunksDeleted = n
		return nil
	})

	return chunksDeleted, err
}

// CountOrphanChunks returns the number of chunks whose document no longer
// exists. Such rows are left behind by deletes issued before cascade deletes
// were reliable and can otherwise surface as ghost search results.
func (s *SQLiteStorage) CountOrphanChunks() (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM chunks
		WHERE document_id NOT IN (SELECT id FROM documents)`).Scan(&count)
	return count, err
}

// DeleteOrphanChunks removes chunks whose document no longer exists and
// returns how many were removed. Like every mutation it goes through the
// single write worker, so it cannot collide with a queued write for the
// database lock; the single DELETE statement is atomic on its own.
func (s *SQLiteStorage) DeleteOrphanChunks() (int64, error) {
	return s.sendWriteOpCount(opDeleteOrphanChunks, nil)
}

// deleteOrphanChunks runs the orphan-chunk delete. Only the write worker calls
// it. The single statement is atomic, so the busy-retry can safely re-run it in
// full — that retry still matters here because the write queue only serialises
// writes within this process, not against other go-rag processes.
func (s *SQLiteStorage) deleteOrphanChunks() (int64, error) {
	var deleted int64

	err := withBusyRetry(func() error {
		res, err := s.db.Exec(`DELETE FROM chunks WHERE document_id NOT IN (SELECT id FROM documents)`)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		deleted = n
		return nil
	})

	return deleted, err
}

// isBusyError reports whether err is a transient SQLite lock error
// (SQLITE_BUSY or SQLITE_LOCKED) that retrying the statement can resolve.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}

	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			return true
		}
		return false
	}

	// Non-typed driver errors carry the code in their message
	// ("database is locked (5) (SQLITE_BUSY)").
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "SQLITE_LOCKED") ||
		strings.Contains(msg, "database is locked")
}

// withBusyRetry runs fn, retrying while it reports a transient SQLite lock
// error. fn must be safe to re-run from scratch, since it is re-executed in
// full after each failed attempt (any earlier partial work is rolled back by
// the caller's transaction).
func withBusyRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt <= busyRetryAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(busyRetryDelay * time.Duration(attempt))
		}
		err = fn()
		if !isBusyError(err) {
			return err
		}
	}
	return fmt.Errorf("database stayed locked after %d attempts: %w", busyRetryAttempts+1, err)
}

// CreateChunk creates a single chunk (for async processing).
func (s *SQLiteStorage) CreateChunk(chunk *Chunk) error {
	if chunk.ID == "" {
		chunk.ID = uuid.NewString()
	}
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = time.Now().UTC()
	}
	return s.sendWriteOp(opCreateChunk, chunk)
}

// CreateChunks creates multiple chunks in a transaction.
func (s *SQLiteStorage) CreateChunks(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return s.sendWriteOp(opCreateChunks, chunks)
}

// chunkSelect is the column list shared by the chunk queries that feed
// retrieval. All of them join documents (see chunkFrom).
const chunkSelect = `SELECT c.id, c.document_id, c.text, c.chunk_index, c.embedding, c.created_at`

// chunkFrom joins chunks to documents so chunks whose document has been
// deleted are never returned. Retrieval feeds these rows straight to users, so
// an orphaned chunk would otherwise surface as a ghost search result.
const chunkFrom = `FROM chunks c JOIN documents d ON d.id = c.document_id`

// GetChunkByIndex retrieves a single chunk by document ID and chunk index.
func (s *SQLiteStorage) GetChunkByIndex(docID string, index int) (*Chunk, error) {
	row := s.db.QueryRow(chunkSelect+`
		`+chunkFrom+` WHERE c.document_id = ? AND c.chunk_index = ?`, docID, index)

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
	rows, err := s.db.Query(chunkSelect+`
		`+chunkFrom+` WHERE c.document_id = ? ORDER BY c.chunk_index`, docID)
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
	rows, err := s.db.Query(chunkSelect + `
		` + chunkFrom + ` WHERE c.embedding IS NOT NULL`)
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

	rows, err := s.db.Query(chunkSelect+`
		`+chunkFrom+` WHERE c.text LIKE ? LIMIT ?`, likeQuery, limit)
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
	return s.sendWriteOp(opCreateWikiIndex, idx)
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
	return s.sendWriteOp(opDeleteWikiIndex, id)
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
	return s.sendWriteOp(opCreateWikiEntry, entry)
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
	return s.sendWriteOp(opUpdateWikiEntry, entry)
}

// DeleteWikiEntry deletes a wiki entry.
func (s *SQLiteStorage) DeleteWikiEntry(id string) error {
	return s.sendWriteOp(opDeleteWikiEntry, id)
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
