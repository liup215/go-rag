# Progress: go-rag

## What works
- Configuration initialization and management.
- Document ingestion with chunking and embedding batching.
- Duplicate ingestion guard: `add` looks up the exact file path first; an already-indexed path prints the existing doc ID and exits 0, `--force` deletes the existing record(s) (chunks included) and re-indexes. `filepath.Clean` makes `./x` and `x` dedupe to one record.
- Hybrid semantic + BM25 search with optional reranking and query rewriting.
- Chinese/CJK keyword recall: BM25 tokenisation splits contiguous CJK runs into overlapping bigrams (+unigrams) and treats CJK punctuation as token separators, so Chinese queries match chunks containing those substrings.
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
- `SQLiteStorage.SearchByKeyword` pre-filters with a single `LIKE '%<raw query>%'`, so multi-word Chinese queries ("机器学习 算法") return an empty candidate set before BM25 runs; the hybrid path (embedder configured) is unaffected because it loads all chunks.
- Databases written before the `DeleteDocument` fix may contain orphan chunks; `GetAllChunks`/`SearchByKeyword` do not JOIN `documents`, so such legacy orphans still appear in results.
- The modernc SQLite driver ignores the `_journal=WAL&_busy_timeout=5000&_fk=1` DSN params (only `_pragma=...` etc. are supported), so WAL/busy-timeout are not actually enabled; writes are safe because they are serialized through the worker goroutine.

## Recent fixes
- `DeleteDocument` left orphaned chunks: the modernc driver ignores the `_fk` DSN param, so foreign keys were off and `ON DELETE CASCADE` never fired — deleted documents' embedded chunks kept polluting `GetAllChunks`/`SearchByKeyword`. `opDeleteDocument` now removes chunks and the document in one transaction. Regression test `TestDeleteDocumentRemovesChunks` fails without the fix.
- `add` no longer creates duplicate documents for the same file path (see "Duplicate ingestion guard" above).
- BM25 tokenizer no longer treats every rune > 127 as a word character: CJK runs are split into bigrams + unigrams (`splitWordRun`/`cjkNgrams` in `internal/retriever`) and CJK punctuation separates tokens, fixing near-zero Chinese BM25 recall. Covered by `internal/retriever/tokenize_cjk_test.go`.
- `init` command no longer overwrites an existing `config.yaml`; it reports "Configuration already initialized." instead.

## Evolution of decisions
- Storage moved from JSON to SQLite for better concurrency and query capabilities.
- Commands are kept as simple top-level subcommands rather than a nested CLI framework.
