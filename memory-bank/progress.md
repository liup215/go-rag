# Progress: go-rag

## What works
- Configuration initialization and management.
- Document ingestion with chunking and embedding batching.
- Hybrid semantic + BM25 search with optional reranking and query rewriting.
- Chinese/CJK keyword recall: BM25 tokenisation splits contiguous CJK runs into overlapping bigrams (+unigrams) and treats CJK punctuation as token separators, so Chinese queries match chunks containing those substrings.
- Document listing with pagination, search, and filters (`--limit/--offset/--page/--search/--filter`), including total match count.
- Search results enriched with their source document: human output shows Document Name/Path per hit; `search --json` carries `document_name`/`document_path` (resolved per distinct `Chunk.DocumentID` via `GetDocument`, missing documents degrade to empty strings / `(unknown)`).
- Machine-readable JSON output: `search --json` prints `{query, count, results}` (document id/name/path, score, chunk id/index, text) and `list --json` prints `{total, offset, count, documents}`; default human output unchanged.
- Document deletion.
- Chunk lookup by document ID and index (`get-chunk`).
- SQLite storage with WAL mode and serialized writes.
- Agent-managed personal wiki (`go-rag wiki`):
  - Topic indexes (`wiki_indexes`)
  - Full-text wiki entries (`wiki_entries`)
  - Symbolic recall workflow: index-list → list → get
  - File-based body create/update/export workflow

## What's left to build
- Potential enhancements driven by user feedback (e.g., chunk ranges, chunk deletion, date-range filters, wiki tags).

## Current status
- Version: v0.4.0 (in development; last release tag `v0.3.0`).
- Search↔document association and `--json` output for `search`/`list` implemented; all tests pass, `go vet` clean, binary builds, and CLI behaviour was smoke-tested end to end against a seeded SQLite DB.

## Known issues
- Document ingestion requires a configured embedding API key; local no-embedding mode is not supported.
- `SQLiteStorage.SearchByKeyword` pre-filters with a single `LIKE '%<raw query>%'`, so multi-word Chinese queries ("机器学习 算法") return an empty candidate set before BM25 runs; the hybrid path (embedder configured) is unaffected because it loads all chunks.

## Recent fixes
- `reorderArgs` now knows about valueless flags (`booleanFlags`), so `go-rag search --json <query>` no longer swallows the query as the flag's value; `--flag=value` forms pass through untouched.
- BM25 tokenizer no longer treats every rune > 127 as a word character: CJK runs are split into bigrams + unigrams (`splitWordRun`/`cjkNgrams` in `internal/retriever`) and CJK punctuation separates tokens, fixing near-zero Chinese BM25 recall. Covered by `internal/retriever/tokenize_cjk_test.go`.
- `init` command no longer overwrites an existing `config.yaml`; it reports "Configuration already initialized." instead.

## Evolution of decisions
- Storage moved from JSON to SQLite for better concurrency and query capabilities.
- Commands are kept as simple top-level subcommands rather than a nested CLI framework.
