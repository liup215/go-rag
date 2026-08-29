# Progress: go-rag

## What works
- Configuration initialization and management.
- Document ingestion with chunking and embedding batching.
- Duplicate ingestion guard: `add` looks up the exact file path first; an already-indexed path prints the existing doc ID and exits 0, `--force` deletes the existing record(s) (chunks included) and re-indexes. `filepath.Clean` makes `./x` and `x` dedupe to one record.
- Hybrid semantic + BM25 search with optional reranking and query rewriting.
- Chinese/CJK keyword recall: BM25 tokenisation splits contiguous CJK runs into overlapping bigrams (+unigrams) and treats CJK punctuation as token separators, so Chinese queries match chunks containing those substrings.
- Document listing with pagination, search, and filters (`--limit/--offset/--page/--search/--filter`), including total match count.
- Search results enriched with their source document: human output shows Document Name/Path per hit; `search --json` carries `document_name`/`document_path` (resolved per distinct `Chunk.DocumentID` via `GetDocument`, missing documents degrade to empty strings / `(unknown)`).
- Machine-readable JSON output: `search --json` prints `{query, count, results}` (document id/name/path, score, chunk id/index, text) and `list --json` prints `{total, offset, count, documents}`; default human output unchanged.

- Encrypted PDF handling (`internal/parser/pdf.go`): a trailer `/Encrypt` entry routes to pdfcpu, which decrypts owner-password-protected files that open with an empty user password (and rebuilds a damaged xref), then the decrypted copy is parsed and `add` prints a 🔓 notice. Files with a real user password fail with `ErrPDFEncrypted` ("PDF is encrypted (owner password)") plus qpdf/pikepdf decryption hints, and text-less-but-decodable PDFs still report missing text (scanned) — three previously-identical failures are now distinguishable.

- Orphan-chunk safety: `delete` removes the document and all of its chunks in one transaction (retried while SQLite reports SQLITE_BUSY/SQLITE_LOCKED, rolled back on any failure) and reports the number of chunks removed; every chunk query joins `documents`, so orphaned chunks never surface in search or `get-chunk`; `go-rag gc [--dry-run]` reclaims orphans already sitting in a database; foreign keys are now actually enforced so new orphans cannot be created.
- Delete status reporting: the CLI verifies the document exists **before** deleting (`deleteDocumentChecked`), so an unknown ID exits 1 with "Error: document <id> not found" while deleting a document without chunks succeeds with `(0 chunk(s) removed)` — the old post-delete check could not tell the two apart and misreported the latter.
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

- Encrypted-PDF recognition/decryption implemented; all tests pass, `go vet` clean, binary builds, and CLI behaviour was smoke-tested end to end against pdfcpu-encrypted fixtures (empty user password → auto-decrypt + index; user password → explicit error + hints; encrypted text-less → missing-text error).

- Orphan-chunk fix (cascade delete + retrieval JOINs + `gc`) implemented; all tests pass, `go vet` clean, binary builds, and CLI behaviour was smoke-tested end to end against a seeded SQLite DB.

## Known issues
- Document ingestion requires a configured embedding API key; local no-embedding mode is not supported.
- `SQLiteStorage.SearchByKeyword` pre-filters with a single `LIKE '%<raw query>%'`, so multi-word Chinese queries ("机器学习 算法") return an empty candidate set before BM25 runs; the hybrid path (embedder configured) is unaffected because it loads all chunks.
- Databases written before the foreign-key fix may still contain orphan chunks; `go-rag gc` cleans them (retrieval already hides them).
- gopdf v0.9.5 lexer bug (worked around, not fixed upstream): `readKeyword` returns an empty keyword *without advancing* for the delimiters `NextToken` does not handle (`)`, `{`, `}`), so `ExtractPageText` spins forever on content it cannot tokenize (e.g. ciphertext of an encrypted stream). `readablePageContent` in `internal/parser/pdf.go` pre-scans each page with gopdf's own lexer and skips pages that hit it; worth reporting upstream.

## Recent fixes
- PDFs whose content gopdf cannot decode no longer hang the parser (`readablePageContent` guard, empty-keyword detection) and are skipped per page; when gopdf fails on structure or garbage, pdfcpu gets a second opinion before "no text extracted" is reported.

- Databases written before the foreign-key fix may still contain orphan chunks; `go-rag gc` cleans them (retrieval already hides them).

## Recent fixes
- `delete` of a chunk-less document was reported as "Error: document <id> not found" (exit 1) even though the delete had succeeded: the existence check ran **after** the delete, when the document was gone either way and an unknown ID was indistinguishable from a 0-chunk document. Fixed by checking existence first in `deleteDocumentChecked` (unknown IDs still exit 1 before any write; chunk-less documents now succeed). Covered by `TestDeleteDocumentChecked` (stub: check precedes delete, unknown ID never reaches `DeleteDocument`) and `TestDeleteDocumentCheckedWithSQLite` (real DB regression).
- Orphan chunks after `delete` (2026-08-23 incident: 17 deleted documents left 69 orphan chunks): the DSN's mattn-style `_fk=1`/`_journal=WAL`/`_busy_timeout` parameters were silently ignored by `modernc.org/sqlite`, so foreign keys — and the schema's `ON DELETE CASCADE` — were never enabled. Fixed by moving to `_pragma=` parameters, adding an explicit chunks+documents delete inside one transaction with a bounded SQLITE_BUSY retry, JOINing `documents` in every chunk-loading query, and adding `go-rag gc` for historical data.
- `reorderArgs` now knows about valueless flags (`booleanFlags`), so `go-rag search --json <query>` no longer swallows the query as the flag's value; `--flag=value` forms pass through untouched.
- `DeleteDocument` left orphaned chunks: the modernc driver ignores the `_fk` DSN param, so foreign keys were off and `ON DELETE CASCADE` never fired — deleted documents' embedded chunks kept polluting `GetAllChunks`/`SearchByKeyword`. `opDeleteDocument` now removes chunks and the document in one transaction. Regression test `TestDeleteDocumentRemovesChunks` fails without the fix.
- `add` no longer creates duplicate documents for the same file path (see "Duplicate ingestion guard" above).

- BM25 tokenizer no longer treats every rune > 127 as a word character: CJK runs are split into bigrams + unigrams (`splitWordRun`/`cjkNgrams` in `internal/retriever`) and CJK punctuation separates tokens, fixing near-zero Chinese BM25 recall. Covered by `internal/retriever/tokenize_cjk_test.go`.
- `init` command no longer overwrites an existing `config.yaml`; it reports "Configuration already initialized." instead.

## Evolution of decisions
- Storage moved from JSON to SQLite for better concurrency and query capabilities.
- Commands are kept as simple top-level subcommands rather than a nested CLI framework.
