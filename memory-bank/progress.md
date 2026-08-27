# Progress: go-rag

## What works
- Configuration initialization and management.
- Document ingestion with chunking and embedding batching.
- Hybrid semantic + BM25 search with optional reranking and query rewriting.
- Document listing with pagination, search, and filters (`--limit/--offset/--page/--search/--filter`), including total match count.
- Document deletion.
- Chunk lookup by document ID and index (`get-chunk`).
- SQLite storage with WAL mode and serialized writes.
- Agent-managed personal wiki (`go-rag wiki`):
  - Topic indexes (`wiki_indexes`)
  - Full-text wiki entries (`wiki_entries`)
  - Symbolic recall workflow: index-list → list → get
  - File-based body create/update/export workflow

## What's left to build
- Potential enhancements driven by user feedback (e.g., chunk ranges, JSON output, chunk deletion, date-range filters, wiki tags).

## Current status
- Version: v0.3.0 released (tag `v0.3.0`).
- Paginated/searchable `list` implemented and tested; all tests pass and the binary builds successfully.

## Known issues
- Document ingestion requires a configured embedding API key; local no-embedding mode is not supported.

## Recent fixes
- `init` command no longer overwrites an existing `config.yaml`; it reports "Configuration already initialized." instead.

## Evolution of decisions
- Storage moved from JSON to SQLite for better concurrency and query capabilities.
- Commands are kept as simple top-level subcommands rather than a nested CLI framework.
