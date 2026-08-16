package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
