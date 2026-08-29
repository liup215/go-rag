package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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
