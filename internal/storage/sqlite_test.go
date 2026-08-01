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

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
