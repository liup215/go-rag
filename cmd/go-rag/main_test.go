package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/liup215/go-rag/internal/storage"
)

// newDedupTestStorage opens a throwaway SQLite storage for the duplicate
// detection tests.
func newDedupTestStorage(t *testing.T) storage.Storage {
	t.Helper()
	s, err := storage.NewStorage(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustCreateDocument(t *testing.T, s storage.Storage, id, filePath, status string) {
	t.Helper()
	if err := s.CreateDocument(&storage.Document{
		ID:       id,
		Name:     filepath.Base(filePath),
		FilePath: filePath,
		DocType:  "txt",
		Status:   status,
	}); err != nil {
		t.Fatalf("failed to create document %s: %v", id, err)
	}
}

func TestDocumentsAtPath(t *testing.T) {
	s := newDedupTestStorage(t)

	mustCreateDocument(t, s, "doc-1", "docs/report.pdf", "indexed")
	mustCreateDocument(t, s, "doc-2", "report.pdf", "indexed")
	mustCreateDocument(t, s, "doc-3", "other/notes.md", "failed")

	t.Run("exact match ignores similar paths", func(t *testing.T) {
		docs, err := documentsAtPath(s, "report.pdf")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(docs) != 1 || docs[0].ID != "doc-2" {
			t.Fatalf("expected only doc-2, got %+v", docs)
		}
	})

	t.Run("path prefixes do not match", func(t *testing.T) {
		docs, err := documentsAtPath(s, "docs/report")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(docs) != 0 {
			t.Fatalf("expected no matches for a partial path, got %+v", docs)
		}
	})

	t.Run("unknown path returns no documents", func(t *testing.T) {
		docs, err := documentsAtPath(s, "missing.pdf")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(docs) != 0 {
			t.Fatalf("expected no documents, got %+v", docs)
		}
	})
}

// TestForceReplaceCleansSamePathDocuments mirrors the --force path of
// handleAdd: every existing document at the path is deleted (chunks included),
// while documents at other paths are left untouched.
func TestForceReplaceCleansSamePathDocuments(t *testing.T) {
	s := newDedupTestStorage(t)

	mustCreateDocument(t, s, "dup-1", "report.pdf", "indexed")
	mustCreateDocument(t, s, "dup-2", "report.pdf", "failed")
	mustCreateDocument(t, s, "keep", "other/notes.md", "indexed")

	// give each document one embedded chunk so orphan leftovers are visible
	chunks := []storage.Chunk{
		{ID: "c1", DocumentID: "dup-1", Text: "old chunk 1", Index: 0, Embedding: []float32{0.1}},
		{ID: "c2", DocumentID: "dup-2", Text: "old chunk 2", Index: 0, Embedding: []float32{0.2}},
		{ID: "c3", DocumentID: "keep", Text: "kept chunk", Index: 0, Embedding: []float32{0.3}},
	}
	if err := s.CreateChunks(chunks); err != nil {
		t.Fatalf("failed to create chunks: %v", err)
	}

	existing, err := documentsAtPath(s, "report.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(existing) != 2 {
		t.Fatalf("expected 2 duplicate documents, got %d", len(existing))
	}

	for _, old := range existing {
		if err := s.DeleteDocument(old.ID); err != nil {
			t.Fatalf("failed to delete %s: %v", old.ID, err)
		}
	}

	docs, err := documentsAtPath(s, "report.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected no documents after --force cleanup, got %+v", docs)
	}

	all, err := s.GetAllChunks()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 1 || all[0].DocumentID != "keep" {
		t.Fatalf("expected only the unrelated chunk to remain, got %+v", all)
	}

	kept, err := s.GetDocument("keep")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kept == nil {
		t.Fatal("expected the unrelated document to survive the cleanup")
	}
}

func TestResolveListOffset(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		offset     int
		page       int
		wantOffset int
		wantErr    bool
	}{
		{name: "defaults", limit: 100, offset: 0, page: 0, wantOffset: 0},
		{name: "offset only", limit: 100, offset: 50, page: 0, wantOffset: 50},
		{name: "page overrides offset", limit: 100, offset: 50, page: 2, wantOffset: 100},
		{name: "page three with small limit", limit: 50, offset: 0, page: 3, wantOffset: 100},
		{name: "page one is offset zero", limit: 100, offset: 25, page: 1, wantOffset: 0},
		{name: "no limit with offset", limit: 0, offset: 10, page: 0, wantOffset: 10},
		{name: "negative limit", limit: -1, offset: 0, page: 0, wantErr: true},
		{name: "negative offset", limit: 100, offset: -1, page: 0, wantErr: true},
		{name: "negative page", limit: 100, offset: 0, page: -1, wantErr: true},
		{name: "page with limit zero", limit: 0, offset: 0, page: 2, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveListOffset(tt.limit, tt.offset, tt.page)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveListOffset(%d, %d, %d) error = %v, wantErr %v", tt.limit, tt.offset, tt.page, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got != tt.wantOffset {
				t.Fatalf("resolveListOffset(%d, %d, %d) = %d, want %d", tt.limit, tt.offset, tt.page, got, tt.wantOffset)
			}
		})
	}
}

func TestFilterFlagsSet(t *testing.T) {
	t.Run("single filter", func(t *testing.T) {
		f := filterFlags{}
		if err := f.Set("status=indexed"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string][]string{"status": {"indexed"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	t.Run("repeated key accumulates values", func(t *testing.T) {
		f := filterFlags{}
		for _, v := range []string{"status=indexed", "status=failed", "type=pdf"} {
			if err := f.Set(v); err != nil {
				t.Fatalf("unexpected error for %q: %v", v, err)
			}
		}
		want := map[string][]string{"status": {"indexed", "failed"}, "type": {"pdf"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	t.Run("whitespace is trimmed", func(t *testing.T) {
		f := filterFlags{}
		if err := f.Set(" status = indexed "); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string][]string{"status": {"indexed"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	t.Run("value may contain equals", func(t *testing.T) {
		f := filterFlags{}
		if err := f.Set("name=a=b.txt"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string][]string{"name": {"a=b.txt"}}
		if !reflect.DeepEqual(map[string][]string(f), want) {
			t.Fatalf("got %v, want %v", map[string][]string(f), want)
		}
	})

	invalid := []string{"status", "=indexed", "status=", "  =  "}
	for _, v := range invalid {
		t.Run("rejects "+v, func(t *testing.T) {
			f := filterFlags{}
			if err := f.Set(v); err == nil {
				t.Fatalf("expected error for %q", v)
			}
		})
	}
}

func TestFilterFlagsString(t *testing.T) {
	if got := (filterFlags{}).String(); got != "" {
		t.Fatalf("empty filters should render as empty string, got %q", got)
	}

	f := filterFlags{"status": {"indexed"}, "type": {"pdf"}}
	// Keys must be sorted so the output is deterministic.
	if got, want := f.String(), "status=indexed, type=pdf"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
