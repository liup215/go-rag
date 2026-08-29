package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// createTestDocument inserts a document with the given attributes.
// The caller should space out creations if ordering matters, because
// CreateDocument stamps created_at with the current time.
func createTestDocument(t *testing.T, s *SQLiteStorage, id, name, filePath, docType, status string) {
	t.Helper()
	doc := &Document{
		ID:       id,
		Name:     name,
		FilePath: filePath,
		DocType:  docType,
		Status:   status,
	}
	if err := s.CreateDocument(doc); err != nil {
		t.Fatalf("failed to create document %s: %v", id, err)
	}
}

func documentIDs(docs []Document) []string {
	ids := make([]string, 0, len(docs))
	for _, doc := range docs {
		ids = append(ids, doc.ID)
	}
	return ids
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, id := range a {
		counts[id]++
	}
	for _, id := range b {
		counts[id]--
		if counts[id] < 0 {
			return false
		}
	}
	return true
}

func TestListDocumentsPagination(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	for i := 1; i <= 5; i++ {
		createTestDocument(t, s, fmt.Sprintf("doc-%d", i), "doc", "doc", "txt", "indexed")
		time.Sleep(5 * time.Millisecond) // ensure distinct created_at values
	}

	all, err := s.ListDocuments(DocumentQuery{})
	if err != nil {
		t.Fatalf("failed to list documents: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 documents, got %d", len(all))
	}

	// Newest first ordering.
	for i := 1; i < len(all); i++ {
		if all[i-1].CreatedAt.Before(all[i].CreatedAt) {
			t.Fatalf("documents not ordered newest first: %v before %v", all[i-1].CreatedAt, all[i].CreatedAt)
		}
	}
	expectedOrder := []string{"doc-5", "doc-4", "doc-3", "doc-2", "doc-1"}
	if !sameIDs(documentIDs(all), expectedOrder) {
		t.Fatalf("unexpected order: %v", documentIDs(all))
	}

	// Page through with limit 2.
	page1, err := s.ListDocuments(DocumentQuery{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("failed to list page 1: %v", err)
	}
	page2, err := s.ListDocuments(DocumentQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("failed to list page 2: %v", err)
	}
	page3, err := s.ListDocuments(DocumentQuery{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("failed to list page 3: %v", err)
	}

	if len(page1) != 2 || len(page2) != 2 || len(page3) != 1 {
		t.Fatalf("unexpected page sizes: %d, %d, %d", len(page1), len(page2), len(page3))
	}
	if !sameIDs(documentIDs(page1), []string{"doc-5", "doc-4"}) {
		t.Fatalf("unexpected page 1: %v", documentIDs(page1))
	}
	if !sameIDs(documentIDs(page2), []string{"doc-3", "doc-2"}) {
		t.Fatalf("unexpected page 2: %v", documentIDs(page2))
	}
	if !sameIDs(documentIDs(page3), []string{"doc-1"}) {
		t.Fatalf("unexpected page 3: %v", documentIDs(page3))
	}

	// Offset beyond the result set is empty.
	extra, err := s.ListDocuments(DocumentQuery{Limit: 2, Offset: 10})
	if err != nil {
		t.Fatalf("failed to list beyond result set: %v", err)
	}
	if len(extra) != 0 {
		t.Fatalf("expected no documents beyond result set, got %d", len(extra))
	}

	// Offset without limit still skips rows.
	rest, err := s.ListDocuments(DocumentQuery{Offset: 3})
	if err != nil {
		t.Fatalf("failed to list with offset only: %v", err)
	}
	if !sameIDs(documentIDs(rest), []string{"doc-2", "doc-1"}) {
		t.Fatalf("unexpected remaining documents: %v", documentIDs(rest))
	}
}

func TestListDocumentsSearchAndFilter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	createTestDocument(t, s, "doc-1", "annual-report.pdf", "docs/annual-report.pdf", "pdf", "indexed")
	time.Sleep(5 * time.Millisecond)
	createTestDocument(t, s, "doc-2", "meeting-notes.md", "notes/meeting-notes.md", "md", "indexed")
	time.Sleep(5 * time.Millisecond)
	createTestDocument(t, s, "doc-3", "50%_off.txt", "promo/50%_off.txt", "txt", "failed")

	tests := []struct {
		name     string
		query    DocumentQuery
		expected []string
	}{
		{
			name:     "search by name",
			query:    DocumentQuery{Search: "report"},
			expected: []string{"doc-1"},
		},
		{
			name:     "search by file path",
			query:    DocumentQuery{Search: "notes/meeting"},
			expected: []string{"doc-2"},
		},
		{
			name:     "search is case insensitive",
			query:    DocumentQuery{Search: "ANNUAL"},
			expected: []string{"doc-1"},
		},
		{
			name:     "search escapes LIKE wildcards",
			query:    DocumentQuery{Search: "50%"},
			expected: []string{"doc-3"},
		},
		{
			name:     "search with no match",
			query:    DocumentQuery{Search: "does-not-exist"},
			expected: nil,
		},
		{
			name:     "filter by status",
			query:    DocumentQuery{Filters: map[string][]string{"status": {"indexed"}}},
			expected: []string{"doc-1", "doc-2"},
		},
		{
			name:     "filter by multiple statuses",
			query:    DocumentQuery{Filters: map[string][]string{"status": {"indexed", "failed"}}},
			expected: []string{"doc-1", "doc-2", "doc-3"},
		},
		{
			name:     "filter by type",
			query:    DocumentQuery{Filters: map[string][]string{"type": {"pdf"}}},
			expected: []string{"doc-1"},
		},
		{
			name:     "filter by name",
			query:    DocumentQuery{Filters: map[string][]string{"name": {"meeting-notes.md"}}},
			expected: []string{"doc-2"},
		},
		{
			name:     "filter by path",
			query:    DocumentQuery{Filters: map[string][]string{"path": {"promo/50%_off.txt"}}},
			expected: []string{"doc-3"},
		},
		{
			name: "search combined with filter",
			query: DocumentQuery{
				Search:  "report",
				Filters: map[string][]string{"status": {"indexed"}},
			},
			expected: []string{"doc-1"},
		},
		{
			name: "filters combined across keys",
			query: DocumentQuery{
				Filters: map[string][]string{"status": {"indexed"}, "type": {"md"}},
			},
			expected: []string{"doc-2"},
		},
		{
			name: "filter with empty value list is ignored",
			query: DocumentQuery{
				Filters: map[string][]string{"status": {}},
			},
			expected: []string{"doc-1", "doc-2", "doc-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs, err := s.ListDocuments(tt.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !sameIDs(documentIDs(docs), tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, documentIDs(docs))
			}
		})
	}
}

func TestCountDocuments(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	createTestDocument(t, s, "doc-1", "a.pdf", "a.pdf", "pdf", "indexed")
	createTestDocument(t, s, "doc-2", "b.md", "b.md", "md", "indexed")
	createTestDocument(t, s, "doc-3", "c.txt", "c.txt", "txt", "failed")

	total, err := s.CountDocuments(DocumentQuery{})
	if err != nil {
		t.Fatalf("failed to count documents: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected total 3, got %d", total)
	}

	// Limit and offset must not affect the count.
	count, err := s.CountDocuments(DocumentQuery{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("failed to count documents: %v", err)
	}
	if count != 3 {
		t.Fatalf("count should ignore limit/offset, expected 3, got %d", count)
	}

	count, err = s.CountDocuments(DocumentQuery{Search: "pdf", Filters: map[string][]string{"status": {"indexed"}}})
	if err != nil {
		t.Fatalf("failed to count documents: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected filtered count 1, got %d", count)
	}
}

func TestDocumentQueryValidation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	if _, err := s.ListDocuments(DocumentQuery{Limit: -1}); err == nil {
		t.Fatal("expected error for negative limit")
	}
	if _, err := s.ListDocuments(DocumentQuery{Offset: -1}); err == nil {
		t.Fatal("expected error for negative offset")
	}
	if _, err := s.ListDocuments(DocumentQuery{Filters: map[string][]string{"bogus": {"x"}}}); err == nil {
		t.Fatal("expected error for unsupported filter key")
	}
	if _, err := s.CountDocuments(DocumentQuery{Filters: map[string][]string{"bogus": {"x"}}}); err == nil {
		t.Fatal("expected error for unsupported filter key in CountDocuments")
	}
}

func TestGetChunkByIndex(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	doc := &Document{
		ID:        "doc-1",
		Name:      "test.txt",
		FilePath:  "test.txt",
		DocType:   "txt",
		Status:    "indexed",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateDocument(doc); err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	chunks := []Chunk{
		{ID: "chunk-0", DocumentID: "doc-1", Text: "first chunk", Index: 0, CreatedAt: time.Now().UTC()},
		{ID: "chunk-1", DocumentID: "doc-1", Text: "second chunk", Index: 1, CreatedAt: time.Now().UTC()},
	}
	if err := s.CreateChunks(chunks); err != nil {
		t.Fatalf("failed to create chunks: %v", err)
	}

	chunk, err := s.GetChunkByIndex("doc-1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunk == nil {
		t.Fatal("expected chunk, got nil")
	}
	if chunk.ID != "chunk-1" || chunk.Text != "second chunk" || chunk.Index != 1 {
		t.Fatalf("unexpected chunk: %+v", chunk)
	}
}

func TestGetChunkByIndexNotFound(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	doc := &Document{
		ID:        "doc-1",
		Name:      "test.txt",
		FilePath:  "test.txt",
		DocType:   "txt",
		Status:    "indexed",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateDocument(doc); err != nil {
		t.Fatalf("failed to create document: %v", err)
	}

	chunk, err := s.GetChunkByIndex("doc-1", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunk != nil {
		t.Fatalf("expected nil chunk, got %+v", chunk)
	}
}

// ---- Orphan chunks and cascade deletes -------------------------------------

// seedOrphanChunk inserts a chunk row whose document does not exist, mimicking
// databases written before foreign keys (and cascade deletes) were actually
// enabled. Foreign keys are enforced on every pooled connection, so the insert
// temporarily disables the pragma on a dedicated connection and restores it
// afterwards.
func seedOrphanChunk(t *testing.T, s *SQLiteStorage, id, docID, text string) {
	t.Helper()

	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatalf("failed to get connection: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("failed to disable foreign keys: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		// X'0000803F' is a little-endian float32 1.0, so the orphan is visible
		// to GetAllChunks, which filters on embedding IS NOT NULL.
		`INSERT INTO chunks (id, document_id, text, chunk_index, embedding) VALUES (?, ?, ?, 0, X'0000803F')`,
		id, docID, text); err != nil {
		t.Fatalf("failed to seed orphan chunk: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("failed to re-enable foreign keys: %v", err)
	}
}

func chunkIDs(chunks []Chunk) []string {
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestForeignKeysEnabled(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("failed to read pragma: %v", err)
	}
	if fk != 1 {
		t.Fatalf("expected foreign_keys pragma to be enabled, got %d", fk)
	}

	// The schema's foreign key must reject chunks whose document does not
	// exist, so orphans cannot be created through the storage API.
	chunk := &Chunk{ID: "chunk-orphan", DocumentID: "missing-doc", Text: "ghost", Index: 0}
	if err := s.CreateChunk(chunk); err == nil {
		t.Fatal("expected error creating a chunk for a non-existent document")
	}
}

func TestDeleteDocumentCascadesChunks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	createTestDocument(t, s, "doc-1", "a.txt", "a.txt", "txt", "indexed")
	createTestDocument(t, s, "doc-2", "b.txt", "b.txt", "txt", "indexed")

	chunks := []Chunk{
		{ID: "chunk-0", DocumentID: "doc-1", Text: "first", Index: 0, Embedding: []float32{1}},
		{ID: "chunk-1", DocumentID: "doc-1", Text: "second", Index: 1, Embedding: []float32{1}},
		{ID: "chunk-2", DocumentID: "doc-1", Text: "third", Index: 2, Embedding: []float32{1}},
		{ID: "other-0", DocumentID: "doc-2", Text: "unrelated", Index: 0, Embedding: []float32{1}},
	}
	if err := s.CreateChunks(chunks); err != nil {
		t.Fatalf("failed to create chunks: %v", err)
	}

	deleted, err := s.DeleteDocument("doc-1")
	if err != nil {
		t.Fatalf("failed to delete document: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 chunks deleted, got %d", deleted)
	}

	doc, err := s.GetDocument("doc-1")
	if err != nil {
		t.Fatalf("failed to get document: %v", err)
	}
	if doc != nil {
		t.Fatalf("expected document to be deleted, got %+v", doc)
	}

	remaining, err := s.GetChunksByDocument("doc-1")
	if err != nil {
		t.Fatalf("failed to get chunks: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no chunks left for deleted document, got %v", chunkIDs(remaining))
	}

	all, err := s.GetAllChunks()
	if err != nil {
		t.Fatalf("failed to get all chunks: %v", err)
	}
	if !sameIDs(chunkIDs(all), []string{"other-0"}) {
		t.Fatalf("expected unrelated chunk to survive, got %v", chunkIDs(all))
	}
}

func TestDeleteDocumentUnknown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	deleted, err := s.DeleteDocument("no-such-doc")
	if err != nil {
		t.Fatalf("expected nil error deleting unknown document, got %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 chunks deleted for unknown document, got %d", deleted)
	}
}

func TestChunkQueriesExcludeOrphans(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	createTestDocument(t, s, "doc-1", "a.txt", "a.txt", "txt", "indexed")

	chunks := []Chunk{
		{ID: "chunk-0", DocumentID: "doc-1", Text: "machine learning basics", Index: 0, Embedding: []float32{1}},
		{ID: "chunk-1", DocumentID: "doc-1", Text: "deep learning details", Index: 1, Embedding: []float32{1}},
	}
	if err := s.CreateChunks(chunks); err != nil {
		t.Fatalf("failed to create chunks: %v", err)
	}

	seedOrphanChunk(t, s, "orphan-0", "deleted-doc", "machine learning ghost")
	seedOrphanChunk(t, s, "orphan-1", "deleted-doc", "another ghost")

	// GetAllChunks feeds the hybrid search path.
	all, err := s.GetAllChunks()
	if err != nil {
		t.Fatalf("failed to get all chunks: %v", err)
	}
	if !sameIDs(chunkIDs(all), []string{"chunk-0", "chunk-1"}) {
		t.Fatalf("GetAllChunks returned orphan chunks: %v", chunkIDs(all))
	}

	// SearchByKeyword feeds the BM25-only keyword path. "learning" matches
	// both live chunks and, without the join, the "machine learning ghost"
	// orphan as well.
	byKeyword, err := s.SearchByKeyword("learning", 50)
	if err != nil {
		t.Fatalf("failed to search by keyword: %v", err)
	}
	if !sameIDs(chunkIDs(byKeyword), []string{"chunk-0", "chunk-1"}) {
		t.Fatalf("SearchByKeyword returned orphan chunks: %v", chunkIDs(byKeyword))
	}

	// GetChunksByDocument feeds the document-scoped keyword path.
	byDoc, err := s.GetChunksByDocument("deleted-doc")
	if err != nil {
		t.Fatalf("failed to get chunks by document: %v", err)
	}
	if len(byDoc) != 0 {
		t.Fatalf("GetChunksByDocument returned orphan chunks: %v", chunkIDs(byDoc))
	}

	// GetChunkByIndex feeds `go-rag get-chunk`.
	chunk, err := s.GetChunkByIndex("deleted-doc", 0)
	if err != nil {
		t.Fatalf("failed to get chunk by index: %v", err)
	}
	if chunk != nil {
		t.Fatalf("expected orphan chunk to be invisible, got %+v", chunk)
	}
}

func TestCountAndDeleteOrphanChunks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	createTestDocument(t, s, "doc-1", "a.txt", "a.txt", "txt", "indexed")
	chunks := []Chunk{
		{ID: "chunk-0", DocumentID: "doc-1", Text: "keep me", Index: 0, Embedding: []float32{1}},
	}
	if err := s.CreateChunks(chunks); err != nil {
		t.Fatalf("failed to create chunks: %v", err)
	}
	// 69 orphans, as in the production incident that motivated this fix.
	for i := 0; i < 69; i++ {
		seedOrphanChunk(t, s, fmt.Sprintf("orphan-%d", i), "deleted-doc", "ghost text")
	}

	count, err := s.CountOrphanChunks()
	if err != nil {
		t.Fatalf("failed to count orphan chunks: %v", err)
	}
	if count != 69 {
		t.Fatalf("expected 69 orphan chunks, got %d", count)
	}

	deleted, err := s.DeleteOrphanChunks()
	if err != nil {
		t.Fatalf("failed to delete orphan chunks: %v", err)
	}
	if deleted != 69 {
		t.Fatalf("expected 69 orphans deleted, got %d", deleted)
	}

	count, err = s.CountOrphanChunks()
	if err != nil {
		t.Fatalf("failed to recount orphan chunks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 orphan chunks after gc, got %d", count)
	}

	all, err := s.GetAllChunks()
	if err != nil {
		t.Fatalf("failed to get all chunks: %v", err)
	}
	if !sameIDs(chunkIDs(all), []string{"chunk-0"}) {
		t.Fatalf("expected live chunk to survive gc, got %v", chunkIDs(all))
	}
}

func TestWikiIndexCRUD(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	idx := &WikiIndex{Title: "Architecture", Description: "Design decisions"}
	if err := s.CreateWikiIndex(idx); err != nil {
		t.Fatalf("failed to create index: %v", err)
	}
	if idx.ID == "" {
		t.Fatal("expected index id to be generated")
	}

	fetched, err := s.GetWikiIndex(idx.ID)
	if err != nil {
		t.Fatalf("failed to get index: %v", err)
	}
	if fetched == nil || fetched.Title != "Architecture" {
		t.Fatalf("unexpected index: %+v", fetched)
	}

	indexes, err := s.ListWikiIndexes()
	if err != nil {
		t.Fatalf("failed to list indexes: %v", err)
	}
	if len(indexes) != 1 {
		t.Fatalf("expected 1 index, got %d", len(indexes))
	}

	if err := s.DeleteWikiIndex(idx.ID); err != nil {
		t.Fatalf("failed to delete index: %v", err)
	}

	fetched, err = s.GetWikiIndex(idx.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetched != nil {
		t.Fatalf("expected nil index after delete, got %+v", fetched)
	}
}

func TestWikiEntryCRUD(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	idx := &WikiIndex{Title: "Decisions"}
	if err := s.CreateWikiIndex(idx); err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	entry := &WikiEntry{
		IndexID: idx.ID,
		Title:   "Chose SQLite WAL",
		Body:    "Detailed reasoning about WAL mode.",
	}
	if err := s.CreateWikiEntry(entry); err != nil {
		t.Fatalf("failed to create entry: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("expected entry id to be generated")
	}

	fetched, err := s.GetWikiEntry(entry.ID)
	if err != nil {
		t.Fatalf("failed to get entry: %v", err)
	}
	if fetched == nil || fetched.Title != "Chose SQLite WAL" || fetched.Body != "Detailed reasoning about WAL mode." {
		t.Fatalf("unexpected entry: %+v", fetched)
	}

	entries, err := s.ListWikiEntries(idx.ID)
	if err != nil {
		t.Fatalf("failed to list entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry.Title = "Chose SQLite WAL mode"
	entry.Body = "Updated reasoning."
	if err := s.UpdateWikiEntry(entry); err != nil {
		t.Fatalf("failed to update entry: %v", err)
	}

	fetched, err = s.GetWikiEntry(entry.ID)
	if err != nil {
		t.Fatalf("failed to get entry after update: %v", err)
	}
	if fetched.Title != "Chose SQLite WAL mode" || fetched.Body != "Updated reasoning." {
		t.Fatalf("entry not updated: %+v", fetched)
	}

	if err := s.DeleteWikiEntry(entry.ID); err != nil {
		t.Fatalf("failed to delete entry: %v", err)
	}

	fetched, err = s.GetWikiEntry(entry.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetched != nil {
		t.Fatalf("expected nil entry after delete, got %+v", fetched)
	}
}

func TestWikiEntryCascadeDelete(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	idx := &WikiIndex{Title: "Temp"}
	if err := s.CreateWikiIndex(idx); err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	entry := &WikiEntry{IndexID: idx.ID, Title: "T", Body: "B"}
	if err := s.CreateWikiEntry(entry); err != nil {
		t.Fatalf("failed to create entry: %v", err)
	}

	if err := s.DeleteWikiIndex(idx.ID); err != nil {
		t.Fatalf("failed to delete index: %v", err)
	}

	fetched, err := s.GetWikiEntry(entry.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetched != nil {
		t.Fatalf("expected entry to be cascade deleted, got %+v", fetched)
	}
}

func TestDeleteDocumentRemovesChunks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer s.Close()

	createTestDocument(t, s, "doc-1", "gone.txt", "gone.txt", "txt", "indexed")
	createTestDocument(t, s, "doc-2", "kept.txt", "kept.txt", "txt", "indexed")

	chunks := []Chunk{
		// Embeddings matter: GetAllChunks only returns chunks that have one,
		// so a surviving orphan would keep polluting search results.
		{ID: "chunk-1", DocumentID: "doc-1", Text: "orphaned?", Index: 0, Embedding: []float32{0.1, 0.2}},
		{ID: "chunk-2", DocumentID: "doc-2", Text: "kept chunk", Index: 0, Embedding: []float32{0.3, 0.4}},
	}
	if err := s.CreateChunks(chunks); err != nil {
		t.Fatalf("failed to create chunks: %v", err)
	}

	if err := s.DeleteDocument("doc-1"); err != nil {
		t.Fatalf("failed to delete document: %v", err)
	}

	gone, err := s.GetChunksByDocument("doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gone) != 0 {
		t.Fatalf("expected no chunks left for deleted document, got %d", len(gone))
	}

	all, err := s.GetAllChunks()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 1 || all[0].DocumentID != "doc-2" {
		t.Fatalf("expected only doc-2's chunk to remain, got %+v", all)
	}

	doc, err := s.GetDocument("doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc != nil {
		t.Fatalf("expected document to be deleted, got %+v", doc)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
