# Active Context: go-rag

## Current work focus

Keyword-only ingestion (BM25-only mode): `go-rag add` no longer exits 1 when
`cfg.Embedding.APIKey` is empty. Parse + chunk run as usual, the embedding
worker pipeline is skipped, and `indexKeywordOnly(store, docID, chunks)` writes
the chunks via the existing `store.CreateChunks` (empty `Embedding` → SQL NULL)
then marks the document `indexed`. Stdout prints the prominent notice
"⚠ 未配置 embedding API key，已按关键词-only 模式索引（BM25），向量搜索不可用"
plus a `--force`-rebuild hint. `search` needed no structural change (`retriever.Search`
already goes BM25-only when the embedder is nil); it now prints an
"ℹ 当前为关键词-only（BM25）检索" notice on **stderr** so `--json` stays clean.
`handleAdd` also gained `defer store.Close()` like the other handlers.

Tests (`cmd/go-rag/main_test.go`): `TestIndexKeywordOnly` (NULL-embedding chunks,
`indexed` status, `GetAllChunks` empty / `SearchByKeyword` hits),
`TestIndexKeywordOnlyFailure` (FK-failing batch leaves status `indexing`),
`TestAddWithoutEmbeddingKeyIndexesKeywordOnly` (subprocess e2e: exit 0, stdout
notice, NULL-embedding chunks, `indexed`, duplicate guard still skips, still one
document), `TestSearchKeywordOnlyNotice` (stderr notice + BM25 JSON hits). A new
`TestMain` lets the test binary re-run itself as the CLI (`GO_RAG_TEST_RUN_MAIN=1`),
so the `os.Exit`-heavy handlers are tested without building a separate binary.
Verified end to end with the built binary: no-key add → indexed + notice; repeat
add → duplicate skip; search → BM25 hit + stderr notice; with a key and a dead
endpoint the original worker path still runs and cleans up.

## Previous round (delete status fix + storage hardening)

Orphan-chunk fix: `delete` now removes a document and all of its chunks in one
transaction, every retrieval query joins `documents` so orphaned chunks can
never surface as ghost results, and a new `go-rag gc` command cleans up
orphans left in databases written by older versions.
Follow-up CLI fix on top of the orphan-chunk work: `go-rag delete` now verifies
the document exists **before** deleting (`deleteDocumentChecked`), so an unknown
ID still exits 1 with "Error: document <id> not found", but deleting a document
that exists without chunks (e.g. left behind by an interrupted add) succeeds
with `(0 chunk(s) removed)` instead of a false "not found".

## Recent changes
- Keyword-only round: `handleAdd`'s empty-key `os.Exit(1)` became a
  `keywordOnly` flag; the degrade branch sits right after the chunks are
  prepared and returns early, so the embedding worker code is untouched.
  `indexKeywordOnly` is the only new helper; search only gained the stderr
  notice. `handleAdd` now releases storage with `defer store.Close()` (matching
  `handleDelete`/`handleGC`).
- `handleDelete` used to treat `chunksDeleted == 0` as proof the document was
  missing — but the check ran after the delete, by which time the document was
  gone either way. Reproduced end to end: a 0-chunk document disappeared from
  `list`, yet the command printed `Error: document seed-0chunk not found` and
  exited 1, which would break agent/script callers of `delete`.
- `cmd/go-rag/main.go`: new `errDocumentNotFound` sentinel and
  `deleteDocumentChecked(store, id)` helper (lookup → not-found error or
  `DeleteDocument`); `handleDelete` maps the sentinel to the "not found"
  message and everything else to "Error deleting document: …". Lookup errors
  surface as `checking document <id>: …`.
- Tests (`cmd/go-rag/main_test.go`): `TestDeleteDocumentChecked` — unknown ID
  fails before `DeleteDocument` is attempted (event-order stub), 0-chunk
  document succeeds with 0, chunk count propagates, lookup/delete errors
  surface; `TestDeleteDocumentCheckedWithSQLite` — real-storage regression
  pinning that `DeleteDocument` reports success with 0 chunks for both an
  unknown ID and a chunk-less document.
- Verified end to end against a seeded DB: 0-chunk delete → exit 0
  `(0 chunk(s) removed)`; unknown ID → exit 1 `not found`; 2-chunk delete →
  exit 0 `(2 chunk(s) removed)`; `gc --dry-run` shows no orphans afterwards.

## Storage follow-ups in the same round (uncommitted → this commit)
- `opCreateChunks` (batch chunk insert) and `opDeleteWikiIndex` are multi-
  statement transactions executed by the write worker; both are now wrapped in
  `withBusyRetry` like `deleteDocumentCascade`. Each rolls back on failure, so
  a retry re-runs from scratch and cannot leave partial rows — this makes
  SKILL.md's "batch chunk inserts are retried automatically" claim true instead
  of aspirational.
- `DeleteOrphanChunks` used to `db.Exec` directly, the one mutation bypassing
  the single-writer queue (contradicting the `SQLiteStorage` type comment). It
  now sends `opDeleteOrphanChunks` through `sendWriteOpCount` (the generalized
  former `sendWriteOpCascade`, which any op that reports a removed-row count can
  reuse); the `DELETE` itself moved to `deleteOrphanChunks`, still under
  `withBusyRetry` because the queue only serialises writes within one process.
- `handleDelete`/`handleGC` now `defer store.Close()` like every other handler.

## Previous round (orphan-chunk fix, committed as 3019022)
- **Root cause** (probe-verified): the DSN used mattn-style parameters
  (`_journal=WAL&_busy_timeout=5000&_fk=1`), but `modernc.org/sqlite` only
  honours `?_pragma=<statement>` — everything else is silently ignored. The
  database actually ran with `journal_mode=delete` and `foreign_keys=0`, so the
  schema's `ON DELETE CASCADE` never fired and `DELETE FROM documents` left its
  chunks behind (the 2026-08-23 incident: 17 deleted ESAT documents → 69 orphan
  chunks).
- DSN is now
  `?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)`;
  a probe test confirmed `foreign_keys=1`, WAL, and that orphan inserts are
  rejected.
- `internal/storage/sqlite.go`:
  - `DeleteDocument(id) (int64, error)` — the interface method changed in
    place. It funnels `opDeleteDocument` through the write worker
    (`sendWriteOpCascade`); ops are now `*writeOp` with a `chunksDeleted` field
    the worker fills, read by the caller after the result arrives.
  - `deleteDocumentCascade` runs `DELETE FROM chunks` + `DELETE FROM documents`
    inside one transaction, wrapped in `withBusyRetry` (bounded retries with
    linear backoff on `SQLITE_BUSY`/`SQLITE_LOCKED`); any failure rolls back, so
    a delete never leaves partial state.
  - `CountOrphanChunks` / `DeleteOrphanChunks` support the new `gc` command.
  - All chunk-loading queries (`GetAllChunks`, `SearchByKeyword`,
    `GetChunksByDocument`, `GetChunkByIndex`) share `chunkSelect`/`chunkFrom`
    and `JOIN documents`, so orphans are invisible to `get-chunk` too.
- `internal/retriever/retriever.go`: `hybridSearch`/`keywordSearch` comments
  document the contract that their chunk-loading storage methods join
  `documents`. No retriever-side doc lookups were added (they would be N+1 on
  the hot path).
- `cmd/go-rag/main.go`: `handleDelete` prints
  `Document deleted successfully (N chunk(s) removed).`;
  `handleAdd`'s failure cleanup discards the count. New `handleGC` backs
  `go-rag gc [--dry-run]`; `dry-run` was added to `booleanFlags`.
- Tests: `TestForeignKeysEnabled` (pragma on + orphan insert rejected),
  `TestDeleteDocumentCascadesChunks`, `TestDeleteDocumentUnknown`,
  `TestChunkQueriesExcludeOrphans` (orphans seeded with FK temporarily disabled
  on a dedicated pooled connection, then re-enabled),
  `TestCountAndDeleteOrphanChunks` (69 orphans, as in the incident). The
  retriever mock gained the three changed/added methods.
- README/SKILL document `delete`'s cascade semantics, `gc`, and a
  "Ghost results" troubleshooting entry.

## Next steps
- Candidate follow-up (from known issues): `SQLiteStorage.SearchByKeyword`
  LIKE-pre-filters on the raw query, so multi-word Chinese queries can return
  an empty candidate set; tokenise into AND/OR LIKE clauses. This matters more
  now that keyword-only mode is the default experience without an API key.
- Consider a `go-rag list --filter mode=keyword-only` (or a status suffix) so
  users can find documents that still need re-embedding after configuring a key.
- Consider upstreaming/reporting the gopdf lexer stall (empty keyword, no
  position advance) — go-rag now guards around it locally.
- Consider `--json` for `get-chunk` and `wiki` subcommands if scripting demand
  appears.
- Environment: `go test -race ./internal/index` fails on this machine with
  "Access is denied" while opening the staged `index.test.exe` — reproduces on
  a pristine HEAD checkout, so it is AV/tooling, not a code race; the package
  passes plain `go test` and `-race` is green for every other package.

## Active decisions
- Keyword-only ingestion is a **degrade**, not an error: an empty embedding key
  must not block ingest, because BM25 retrieval alone is already useful. The
  document is marked `indexed` (true — keyword search finds it) and the
  limitation is communicated by the stdout warning, not by a fake status.
- Chunk storage reuses the existing `CreateChunks` path; NULL embeddings are
  already the storage layer's representation of "not embedded" and
  `GetAllChunks`'s `embedding IS NOT NULL` filter keeps them out of vector
  search, so no storage change was needed.
- The keyword-only notice goes to **stdout** (an indexing outcome, visible in
  normal terminal use), while the search-side notice goes to **stderr** (so
  `search --json` output stays machine-readable).
- Duplicate-guard / `--force` semantics are unchanged and apply in keyword-only
  mode too — the mode choice happens after the guard, so an already-indexed
  path is never silently re-written (and never half-indexed) because of a
  missing key.
- `indexKeywordOnly` is a testable helper (store in, error out) following the
  `deleteDocumentChecked` precedent: exit semantics stay in the handler,
  behaviour is pinned in tests. The e2e test drives the real `os.Exit`-heavy
  handler by re-running the test binary as the CLI (`GO_RAG_TEST_RUN_MAIN=1` in
  `TestMain`) rather than building a separate binary — no build step, no
  sandbox issues, and subprocesses keep stdout/stderr/exit codes inspectable.
- Every mutation goes through the single write worker — `DeleteOrphanChunks`
  included. The queue cannot serialise against *other processes*, which is why
  worker-side multi-statement writes keep their `withBusyRetry` wrapper; single-
  statement reads (`CountOrphanChunks`) stay on the pool.
- Existence is checked in the CLI **before** `DeleteDocument`, not derived from
  the delete result: the storage call cannot distinguish an unknown ID from a
  chunk-less document, and a post-delete lookup cannot either. A document that
  vanishes between check and delete (concurrent winner) still reports success —
  the end state matches the request.
- `deleteDocumentChecked` is a testable helper (storage-in, error out) so the
  ordering and exit semantics are pinned by unit tests instead of living inside
  the `os.Exit`-heavy handler.
- Orphan filtering lives in SQL (`chunkFrom` JOIN), consistent with the
  existing "filtering/searching lives in the storage layer" rule; the retriever
  stays storage-agnostic and documents the contract at the call sites.
- `DeleteDocument`'s signature changed in place (returning the removed chunk
  count) rather than adding a parallel `DeleteDocumentCascade` method — same
  precedent as `ListDocuments`.
- Explicit `DELETE FROM chunks` inside the transaction is kept even though the
  foreign key is now enforced: pragma state is per-connection and the explicit
  delete also cleans pre-existing orphans when their (missing) document ID is
  deleted via `delete`.
- `gc` is a separate maintenance command, not part of `delete` or startup, so
  normal commands stay read-only with respect to legacy data.
- Encryption is checked *first* (trailer via gopdf, then a scan of the newest xref section), because letting gopdf parse an encrypted file risks the lexer spin; the scan excludes `/EncryptMetadata`. "All pages decoded, still no text" is treated as scanned and returns immediately; undecodable pages are skipped, not fatal. pdfcpu (pure-Go) for decryption via `api.Decrypt`. (trailer via gopdf, then a scan of the newest xref section), because letting gopdf parse an encrypted file risks the lexer spin; the scan excludes `/EncryptMetadata`. "All pages decoded, still no text" is treated as scanned and returns immediately; undecodable pages are skipped, not fatal. pdfcpu (pure-Go) for decryption via `api.Decrypt`.
- Previous: dedup reuses the existing `DocumentQuery` `path` exact filter (returns ALL same-path records so `--force` cleans up legacy duplicates in one pass); `--force` deletion deferred until after parse+chunk succeed; skip is a success outcome (exit 0).
- Document lookup for search results stays in the CLI layer via `GetDocument`
  per distinct ID — top-k is small, and it avoids changing the `Storage`
  interface (which would ripple into mocks). Revisit with a single
  `GetDocuments(ids)`/join if top-k or N+1 concerns grow.
- JSON keys are snake_case and stable regardless of hit state (no `omitempty`
  on name/path), so consumers get one schema; empty results marshal as `[]`,
  never `null`.
- JSON output is opt-in per command (`--json`); human-readable output is
  byte-for-byte unchanged apart from the two added document lines in `search`.
- `reorderArgs`' bool-flag list (`booleanFlags`) is a package-level map rather
  than a per-FlagSet parameter: flags are global to this CLI and it keeps the
  helper's signature unchanged.
- Previous: filtering/searching lives in the storage layer (SQL WHERE), not in
  the CLI; pagination defaults stay in the CLI (`--limit 100`); `0` means "no
  limit"; the `Storage` interface was changed in place rather than adding a
  parallel filtered method.

## Previous work
- Encrypted PDF recognition/decryption (task 7a6c7d91): trailer `/Encrypt` routes to pdfcpu (empty-user-password decrypt + xref rebuild), decrypted copy parsed with `Decrypted: true`; real user password fails with `ErrPDFEncrypted` + qpdf/pikepdf hints. Found+worked around a gopdf v0.9.5 lexer bug (`readKeyword` emits a non-advancing empty keyword for `)`/`{`/`}`, so `ExtractPageText` spins forever on undecodable content) — `readablePageContent` pre-scans and skips such pages; worth reporting upstream.
- Duplicate ingestion guard (task 64ea4259): `add` skips an already-indexed file path (exit 0, `printDuplicateNotice`) instead of creating a second document; `--force` deletes existing same-path record(s) only after parse+chunk succeed. Tests: `TestDeleteDocumentRemovesChunks`, `TestDocumentsAtPath`, `TestForceReplaceCleansSamePathDocuments`.
- Search results identify their source document (`document_name`/`path`), and
  `search`/`list` gained `--json` output with a stable schema (`[]`, never
  `null`); `reorderArgs` learned boolean flags so `search --json <query>`
  keeps the query.
- CJK/BM25 tokenisation fix (bigrams + unigrams, punctuation separators) so
  Chinese keyword queries actually recall chunks.
- Personal wiki subsystem (`go-rag wiki`) with `wiki_indexes`/`wiki_entries`
  tables, symbolic recall flow (index-list → list → get), and file-based body
  create/update/export.

