package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/liup215/go-rag/internal/parser"
	"github.com/liup215/go-rag/internal/storage"
)

// docLookupStub satisfies storage.Storage by embedding the interface and
// overriding only the method used to enrich search output.
type docLookupStub struct {
	storage.Storage
	docs  map[string]storage.Document
	calls []string
}

func (s *docLookupStub) GetDocument(id string) (*storage.Document, error) {
	s.calls = append(s.calls, id)
	if doc, ok := s.docs[id]; ok {
		return &doc, nil
	}
	return nil, nil
}

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

func TestLoadDocumentsForResults(t *testing.T) {
	stub := &docLookupStub{docs: map[string]storage.Document{
		"doc-1": {ID: "doc-1", Name: "a.pdf", FilePath: "docs/a.pdf"},
	}}
	results := []storage.SearchResult{
		{Chunk: storage.Chunk{ID: "c1", DocumentID: "doc-1", Index: 0, Text: "one"}, Score: 0.9},
		{Chunk: storage.Chunk{ID: "c2", DocumentID: "doc-1", Index: 1, Text: "two"}, Score: 0.8},
		{Chunk: storage.Chunk{ID: "c3", DocumentID: "doc-2", Index: 0, Text: "three"}, Score: 0.7},
		{Chunk: storage.Chunk{ID: "c4", DocumentID: "", Index: 2, Text: "four"}, Score: 0.6},
	}

	docs, err := loadDocumentsForResults(stub, results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Distinct document IDs only: doc-1 is hit twice but fetched once, and an
	// empty document ID is skipped entirely.
	if got, want := strings.Join(stub.calls, ","), "doc-1,doc-2"; got != want {
		t.Fatalf("GetDocument called for [%s], want [%s]", got, want)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 resolved document IDs, got %d (%v)", len(docs), docs)
	}
	if doc := docs["doc-1"]; doc == nil || doc.Name != "a.pdf" || doc.FilePath != "docs/a.pdf" {
		t.Fatalf("doc-1 not resolved correctly: %+v", doc)
	}
	// A document that no longer exists maps to nil instead of an error.
	if doc := docs["doc-2"]; doc != nil {
		t.Fatalf("missing document should map to nil, got %+v", doc)
	}

	if docs, err := loadDocumentsForResults(stub, nil); err != nil || len(docs) != 0 {
		t.Fatalf("empty results should resolve to no documents, got %v, err %v", docs, err)
	}
}

func TestBuildSearchResultsJSON(t *testing.T) {
	docs := map[string]*storage.Document{
		"doc-1": {ID: "doc-1", Name: "report.pdf", FilePath: "docs/report.pdf"},
		"doc-2": nil,
	}
	results := []storage.SearchResult{
		{Chunk: storage.Chunk{ID: "c1", DocumentID: "doc-1", Index: 3, Text: "hello"}, Score: 0.75},
		{Chunk: storage.Chunk{ID: "c2", DocumentID: "doc-2", Index: 0, Text: "orphan"}, Score: 0.25},
	}

	got := buildSearchResultsJSON(results, docs)
	if len(got) != 2 {
		t.Fatalf("expected 2 JSON results, got %d", len(got))
	}

	want := searchResultJSON{
		DocumentID:   "doc-1",
		DocumentName: "report.pdf",
		DocumentPath: "docs/report.pdf",
		Score:        0.75,
		ChunkID:      "c1",
		ChunkIndex:   3,
		Text:         "hello",
	}
	if got[0] != want {
		t.Fatalf("result[0] = %+v, want %+v", got[0], want)
	}

	// A hit whose document is gone keeps empty name/path but stays in the list.
	if got[1].DocumentName != "" || got[1].DocumentPath != "" {
		t.Fatalf("missing document should yield empty name/path, got %+v", got[1])
	}
	if got[1].DocumentID != "doc-2" || got[1].Text != "orphan" || got[1].Score != 0.25 {
		t.Fatalf("missing-document hit lost its fields: %+v", got[1])
	}

	if empty := buildSearchResultsJSON(nil, docs); empty == nil || len(empty) != 0 {
		t.Fatalf("empty results must marshal as [], got %#v", empty)
	}
}

func TestSearchOutputJSONShape(t *testing.T) {
	results := []storage.SearchResult{
		{Chunk: storage.Chunk{ID: "c1", DocumentID: "doc-1", Index: 2, Text: "hello"}, Score: 0.5},
	}
	payload := searchOutputJSON{
		Query: "machine learning",
		Count: 1,
		Results: buildSearchResultsJSON(results, map[string]*storage.Document{
			"doc-1": {ID: "doc-1", Name: "a.pdf", FilePath: "docs/a.pdf"},
		}),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded["query"] != "machine learning" || decoded["count"] != float64(1) {
		t.Fatalf("unexpected top-level fields: %s", data)
	}
	items, ok := decoded["results"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("results must be a non-empty array: %s", data)
	}
	item, ok := items[0].(map[string]interface{})
	if !ok {
		t.Fatalf("result must be an object: %s", data)
	}
	for _, key := range []string{"document_id", "document_name", "document_path", "score", "chunk_id", "chunk_index", "text"} {
		if _, ok := item[key]; !ok {
			t.Fatalf("search result JSON missing key %q: %s", key, data)
		}
	}
	if item["document_name"] != "a.pdf" || item["document_path"] != "docs/a.pdf" ||
		item["chunk_index"] != float64(2) || item["text"] != "hello" {
		t.Fatalf("unexpected result payload: %s", data)
	}
}

func TestListOutputJSONShape(t *testing.T) {
	payload := listOutputJSON{
		Total:     7,
		Offset:    2,
		Count:     1,
		Documents: nonNilDocuments([]storage.Document{{ID: "doc-1", Name: "a.pdf", FilePath: "docs/a.pdf"}}),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded["total"] != float64(7) || decoded["offset"] != float64(2) || decoded["count"] != float64(1) {
		t.Fatalf("unexpected top-level fields: %s", data)
	}
	docs, ok := decoded["documents"].([]interface{})
	if !ok || len(docs) != 1 {
		t.Fatalf("documents must be a non-empty array: %s", data)
	}
	doc, ok := docs[0].(map[string]interface{})
	if !ok || doc["id"] != "doc-1" || doc["name"] != "a.pdf" || doc["file_path"] != "docs/a.pdf" {
		t.Fatalf("unexpected document payload: %s", data)
	}
}

func TestNonNilDocuments(t *testing.T) {
	if got := nonNilDocuments(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil documents must normalize to an empty slice, got %#v", got)
	}
	docs := []storage.Document{{ID: "doc-1"}}
	if got := nonNilDocuments(docs); !reflect.DeepEqual(got, docs) {
		t.Fatalf("existing slice must pass through unchanged, got %#v", got)
	}
}

// deleteStoreStub satisfies storage.Storage for deleteDocumentChecked tests by
// embedding the interface and overriding only the two methods it uses. events
// records the call order so tests can pin the existence check to before the
// delete.
type deleteStoreStub struct {
	storage.Storage
	exists map[string]bool
	// deleteChunks is what DeleteDocument reports as removed; 0 mirrors the
	// real storage for a document without chunks.
	deleteChunks int64
	getErr       error
	deleteErr    error
	events       []string
}

func (s *deleteStoreStub) GetDocument(id string) (*storage.Document, error) {
	s.events = append(s.events, "get:"+id)
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.exists[id] {
		return &storage.Document{ID: id, Name: "doc.txt", FilePath: "docs/doc.txt", DocType: "txt", Status: "indexed"}, nil
	}
	return nil, nil
}

func (s *deleteStoreStub) DeleteDocument(id string) (int64, error) {
	s.events = append(s.events, "delete:"+id)
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	return s.deleteChunks, nil
}

func TestDeleteDocumentChecked(t *testing.T) {
	t.Run("unknown ID fails without attempting the delete", func(t *testing.T) {
		stub := &deleteStoreStub{exists: map[string]bool{}}

		n, err := deleteDocumentChecked(stub, "missing")
		if !errors.Is(err, errDocumentNotFound) {
			t.Fatalf("err = %v, want errDocumentNotFound", err)
		}
		if n != 0 {
			t.Fatalf("chunks deleted = %d, want 0", n)
		}
		if !reflect.DeepEqual(stub.events, []string{"get:missing"}) {
			t.Fatalf("events = %v, want only the existence check", stub.events)
		}
	})

	// Regression: a document that exists but has no chunks (e.g. left behind by
	// an interrupted add) used to be reported as "not found" — the existence
	// check ran after the delete, when the document was already gone and both
	// cases looked identical.
	t.Run("existing document without chunks deletes successfully", func(t *testing.T) {
		stub := &deleteStoreStub{exists: map[string]bool{"doc-0chunk": true}}

		n, err := deleteDocumentChecked(stub, "doc-0chunk")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Fatalf("chunks deleted = %d, want 0", n)
		}
		if !reflect.DeepEqual(stub.events, []string{"get:doc-0chunk", "delete:doc-0chunk"}) {
			t.Fatalf("events = %v, want check before delete", stub.events)
		}
	})

	t.Run("existing document reports removed chunk count", func(t *testing.T) {
		stub := &deleteStoreStub{
			exists:       map[string]bool{"doc-1": true},
			deleteChunks: 4,
		}

		n, err := deleteDocumentChecked(stub, "doc-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 4 {
			t.Fatalf("chunks deleted = %d, want 4", n)
		}
	})

	t.Run("lookup failure surfaces without deleting", func(t *testing.T) {
		wantErr := errors.New("disk I/O error")
		stub := &deleteStoreStub{getErr: wantErr}

		_, err := deleteDocumentChecked(stub, "doc-1")
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want wrapped %v", err, wantErr)
		}
		if len(stub.events) != 1 {
			t.Fatalf("events = %v, delete must not run when the lookup fails", stub.events)
		}
	})

	t.Run("delete failure surfaces", func(t *testing.T) {
		wantErr := errors.New("database is locked")
		stub := &deleteStoreStub{
			exists:    map[string]bool{"doc-1": true},
			deleteErr: wantErr,
		}

		_, err := deleteDocumentChecked(stub, "doc-1")
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
	})
}

// TestDeleteDocumentCheckedWithSQLite pins the storage behaviour that broke the
// old handleDelete: DeleteDocument reports success with zero chunks for both an
// unknown ID and a chunk-less document, so a post-delete existence check could
// not tell them apart.
func TestDeleteDocumentCheckedWithSQLite(t *testing.T) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer store.Close()

	doc := &storage.Document{ID: "doc-0chunk", Name: "interrupted.txt", FilePath: "/tmp/interrupted.txt", DocType: "txt", Status: "pending"}
	if err := store.CreateDocument(doc); err != nil {
		t.Fatalf("create document: %v", err)
	}

	n, err := deleteDocumentChecked(store, "doc-0chunk")
	if err != nil {
		t.Fatalf("delete 0-chunk document: %v", err)
	}
	if n != 0 {
		t.Fatalf("chunks deleted = %d, want 0", n)
	}
	if got, err := store.GetDocument("doc-0chunk"); err != nil || got != nil {
		t.Fatalf("document should be gone, got %+v, err %v", got, err)
	}

	// Deleting it again — now genuinely absent — must be an error, not success.
	if _, err := deleteDocumentChecked(store, "doc-0chunk"); !errors.Is(err, errDocumentNotFound) {
		t.Fatalf("err = %v, want errDocumentNotFound", err)
	}
	if _, err := deleteDocumentChecked(store, "never-existed"); !errors.Is(err, errDocumentNotFound) {
		t.Fatalf("err = %v, want errDocumentNotFound", err)
	}
}

func TestReorderArgsBooleanFlags(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		// A boolean flag must not swallow the positional argument that follows it.
		{name: "bool flag before positional", in: []string{"--json", "query"}, want: []string{"--json", "query"}},
		{name: "bool flag single dash", in: []string{"-json", "query"}, want: []string{"-json", "query"}},
		{name: "bool flag with explicit value", in: []string{"--json=false", "query"}, want: []string{"--json=false", "query"}},
		{name: "bool flag last", in: []string{"query", "--json"}, want: []string{"--json", "query"}},
		// Value-taking flags keep their existing behaviour.
		{name: "value flag before positional", in: []string{"--top-k", "5", "query"}, want: []string{"--top-k", "5", "query"}},
		{name: "positional after positional", in: []string{"--top-k", "5", "query", "extra"}, want: []string{"--top-k", "5", "query", "extra"}},
		// A value that merely looks like a flag name is still a value.
		{name: "value named like bool flag", in: []string{"--search", "json", "query"}, want: []string{"--search", "json", "query"}},
		{name: "value flag then bool flag", in: []string{"query", "--json", "--top-k", "3"}, want: []string{"--json", "--top-k", "3", "query"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderArgs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("reorderArgs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseFailureHints pins the follow-up advice shown when `add` cannot parse
// a file: encrypted PDFs must point at decryption, everything else PDF-ish at
// scanning/normalization, and path problems must stay hint-free.
func TestParseFailureHints(t *testing.T) {
	pdfPath := filepath.Join("docs", "ap-biology-ced.pdf")
	encrypted := fmt.Errorf("%w: the file does not open with an empty user password", parser.ErrPDFEncrypted)

	tests := []struct {
		name     string
		filePath string
		err      error
		want     []string
	}{
		{
			name:     "encrypted pdf",
			filePath: pdfPath,
			err:      encrypted,
			want:     []string{"qpdf", "pikepdf"},
		},
		{
			name:     "unencrypted pdf parse failure",
			filePath: pdfPath,
			err:      errors.New("no text extracted from pdf"),
			want:     []string{"password-protected", "scanned"},
		},
		{
			name:     "pdf does not exist",
			filePath: pdfPath,
			err:      fmt.Errorf("file not found: %w", fs.ErrNotExist),
			want:     nil,
		},
		{
			name:     "non-pdf file",
			filePath: filepath.Join("notes", "todo.txt"),
			err:      errors.New("no text extracted from pdf"),
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFailureHints(tt.filePath, tt.err)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if len(got) == 0 {
				t.Fatalf("parseFailureHints(%q, %v) returned no hints, want mention of %q", tt.filePath, tt.err, tt.want)
			}
			joined := strings.Join(got, "\n")
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Errorf("hints %q do not mention %q", joined, want)
				}
			}
		})
	}
}
