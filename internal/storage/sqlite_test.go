package storage

import (
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
