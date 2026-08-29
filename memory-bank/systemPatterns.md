# System Patterns: go-rag

## Architecture
- CLI entry point: `cmd/go-rag/main.go` dispatches subcommands via a `switch` on `os.Args[1]`.
- Packages under `internal/` handle specific concerns: storage, chunking, embedding, retrieval, parsing, knowledge graph.
- Configuration: YAML file managed by `pkg/config`.

## Key design patterns
- **Interface-based storage**: `storage.Storage` abstracts the database, currently backed by SQLite.
- **Worker-guarded writes**: SQLite writes are serialized through a single worker goroutine to avoid `SQLITE_BUSY`.
- **Command handlers**: Each subcommand loads config, initializes dependencies, performs work, prints results, and exits on error.
- **Mock implementations**: Retriever tests use an in-memory mock of `storage.Storage`.

## Component relationships
```
cmd/go-rag/main.go
  -> config.Load
  -> storage.NewStorage
  -> chunker / parser / embedder / retriever / wiki
```

## Wiki subsystem pattern
The personal wiki ("wiki") is intentionally separate from the RAG pipeline:
- **Tables**: `wiki_indexes` (topics) and `wiki_entries` (full text).
- **Truth source**: SQLite rows, not Markdown files.
- **Index management**: The external agent decides how indexes are organized and updates them explicitly.
- **Recall flow**: `index-list` → `list <index-id>` → `get <entry-id>`.
- **No embeddings**: Retrieval is symbolic; no chunking or vector search is required.
- **File-based body workflow**: `remember` and `update --file` read body from a file; `export` writes body to a file. This lets users edit large bodies with any editor while keeping SQLite as the truth source.

## CJK keyword retrieval pattern
- `tokenize` in `internal/retriever/retriever.go` is the single tokenizer used by `BuildBM25Index` for both documents and queries, so any tokenisation change automatically applies to both sides — keep them symmetric.
- CJK runs (Han/Kana/Hangul detected via stdlib script tables `unicode.Is(unicode.Han|Hiragana|Katakana|Hangul, r)`) are split into overlapping bigrams plus unigrams (`cjkNgrams`); bigrams carry discriminative power, unigrams keep single-character queries matchable.
- `isWordChar` accepts non-ASCII runes unless they are punctuation/symbol/space/control (Unicode categories), which makes CJK punctuation ('，' '。' '《》') a separator instead of part of a token. Mixed runs are partitioned into maximal CJK / non-CJK sub-runs (`splitWordRun`) so ASCII words stay whole ("GPT4模型" → "gpt4" + 模型 n-grams).
- ASCII behaviour is pinned by tests: `[0-9A-Za-z]` runs are single lower-cased tokens; CJK coverage lives in `internal/retriever/tokenize_cjk_test.go`.
- Known gap: the `keywordSearch` pre-filter `SearchByKeyword` LIKEs the raw query, so multi-word CJK queries can yield an empty candidate set before BM25 runs.

## Critical implementation paths
- Adding a storage query requires updating the interface, SQLite implementation, and any mock implementations.
- CLI flags use `flag.NewFlagSet` plus `reorderArgs` to allow flags after positional arguments. Valueless flags must be listed in `booleanFlags` (package-level map in `cmd/go-rag/main.go`) or `reorderArgs` attaches the next token as the flag's value — `--flag=value` forms are always passed through untouched.
- Wiki deletion cascades manually in a SQLite transaction to avoid relying on per-connection foreign-key pragma state.
- **Document deletion does the same**: `opDeleteDocument` deletes chunks then the document in one transaction. The `chunks.document_id` FK declares `ON DELETE CASCADE`, but the modernc driver ignores the `_fk` DSN param (only `_pragma=...` is supported), so cascade cannot be relied on. Any new parent/child delete must cascade explicitly.
- **Duplicate ingestion guard**: `handleAdd` normalizes the input path with `filepath.Clean`, then `documentsAtPath` queries `DocumentQuery{Filters: {"path": {path}}}` (exact SQL match). Skip = stdout notice + `return` (exit 0) before parsing; `--force` deletes the existing records after parse+chunk succeed and before `CreateDocument`. Keep the decision logic in small helpers (`documentsAtPath`, `printDuplicateNotice`) so it can be unit-tested with real SQLite storage in `cmd/go-rag/main_test.go`.

## Document listing pattern
- `storage.DocumentQuery` (`Search`, `Filters`, `Limit`, `Offset`) is the single input for `ListDocuments` and `CountDocuments`.
- SQL is assembled from a sorted key→column map (`documentFilterColumns`); unknown keys are rejected so bad filters fail loudly.
- `--search` uses `LIKE` with `\` escaping of `%`, `_`, and `\`; SQLite `LIKE` is case-insensitive for ASCII.
- The CLI never filters rows itself: it always sends the same query to `CountDocuments` (total) and `ListDocuments` (page), then prints `Showing n of total (offset o)` and a next-page hint.
- Repeated `--filter key=value` flags accumulate via the `filterFlags` type (a `flag.Value`); repeated keys become SQL `IN` (OR).
- `resolveListOffset(limit, offset, page)` centralizes pagination math and validation; `page` overrides `offset`, `limit 0` means "no limit" and cannot be paged.
- Both helpers are pure and unit-tested in `cmd/go-rag/main_test.go`; keep business logic in such helpers rather than inside handlers that call `os.Exit`.

## Search output & JSON pattern
- Search hits are joined to their document in the CLI layer: `loadDocumentsForResults` queries `GetDocument` once per distinct `Chunk.DocumentID` (top-k is small, so no SQL join / interface change is warranted). The returned map holds nil for documents that no longer exist, and empty IDs are skipped.
- Human output shows `Document ID` / `Document Name` / `Document Path` per hit; unresolvable documents print `(unknown)` rather than hiding the hit.
- `--json` is opt-in per command. `search --json` → `{query, count, results}` with per-hit `document_id/document_name/document_path/score/chunk_id/chunk_index/text`; `list --json` → `{total, offset, count, documents}` reusing `storage.Document`'s snake_case tags.
- Never marshal `storage.Chunk` directly — it would leak embedding vectors. Dedicated JSON structs (`searchResultJSON`, `searchOutputJSON`, `listOutputJSON`) keep `document_name`/`document_path` present-but-empty (stable schema, no `omitempty`) for missing documents.
- Empty collections must serialize as `[]`, not `null` (`buildSearchResultsJSON` / `nonNilDocuments` normalize).
- `writeJSON` emits indented JSON with HTML escaping disabled so paths and chunk text stay readable; JSON mode suppresses human-only messages ("No results found.", footers) so stdout stays pure JSON.
