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

## Critical implementation paths
- Adding a storage query requires updating the interface, SQLite implementation, and any mock implementations.
- CLI flags use `flag.NewFlagSet` plus `reorderArgs` to allow flags after positional arguments.
- Wiki deletion cascades manually in a SQLite transaction to avoid relying on per-connection foreign-key pragma state.

## Document listing pattern
- `storage.DocumentQuery` (`Search`, `Filters`, `Limit`, `Offset`) is the single input for `ListDocuments` and `CountDocuments`.
- SQL is assembled from a sorted key→column map (`documentFilterColumns`); unknown keys are rejected so bad filters fail loudly.
- `--search` uses `LIKE` with `\` escaping of `%`, `_`, and `\`; SQLite `LIKE` is case-insensitive for ASCII.
- The CLI never filters rows itself: it always sends the same query to `CountDocuments` (total) and `ListDocuments` (page), then prints `Showing n of total (offset o)` and a next-page hint.
- Repeated `--filter key=value` flags accumulate via the `filterFlags` type (a `flag.Value`); repeated keys become SQL `IN` (OR).
- `resolveListOffset(limit, offset, page)` centralizes pagination math and validation; `page` overrides `offset`, `limit 0` means "no limit" and cannot be paged.
- Both helpers are pure and unit-tested in `cmd/go-rag/main_test.go`; keep business logic in such helpers rather than inside handlers that call `os.Exit`.
