# Active Context: go-rag

## Current work focus

Orphan-chunk fix: `delete` now removes a document and all of its chunks in one
transaction, every retrieval query joins `documents` so orphaned chunks can
never surface as ghost results, and a new `go-rag gc` command cleans up
orphans left in databases written by older versions.

## Recent changes
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
- `cmd/go-rag/main.go`: `handleDelete` uses the new signature, prints
  `Document deleted successfully (N chunk(s) removed).`, and reports
  `Error: document <id> not found` (exit 1) when the ID matches nothing;
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
  an empty candidate set; tokenise into AND/OR LIKE clauses.
- Consider upstreaming/reporting the gopdf lexer stall (empty keyword, no
  position advance) — go-rag now guards around it locally.
- Consider `--json` for `get-chunk` and `wiki` subcommands if scripting demand
  appears.

## Active decisions
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

