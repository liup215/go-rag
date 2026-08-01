# Progress: go-rag

## What works
- Configuration initialization and management.
- Document ingestion with chunking and embedding batching.
- Hybrid semantic + BM25 search with optional reranking and query rewriting.
- Document listing and deletion.
- Chunk lookup by document ID and index (`get-chunk`).
- SQLite storage with WAL mode and serialized writes.

## What's left to build
- Potential enhancements driven by user feedback (e.g., chunk ranges, JSON output, chunk deletion).

## Current status
- Version: v0.2.2 released (tag `v0.2.2`).
- All tests pass and the binary builds successfully.

## Known issues
- Document ingestion requires a configured embedding API key; local no-embedding mode is not supported.
- README still mentions "JSON storage" in the feature list while the actual storage is SQLite.

## Recent fixes
- `init` command no longer overwrites an existing `config.yaml`; it reports "Configuration already initialized." instead.

## Evolution of decisions
- Storage moved from JSON to SQLite for better concurrency and query capabilities.
- Commands are kept as simple top-level subcommands rather than a nested CLI framework.
