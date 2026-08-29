# Active Context: go-rag

## Current work focus
Duplicate ingestion guard for `go-rag add`: an already-indexed file path is skipped (exit 0) instead of creating a second document, and `--force` deletes the existing record(s) before re-indexing.

## Recent changes
- Root cause of duplicate documents: `handleAdd` created a fresh `documents` row on every run; re-running `go-rag add file.pdf` duplicated the document and all chunks, polluting search and wasting embedding calls.
- `cmd/go-rag/main.go` `handleAdd`:
  - New `--force` flag. The input path is normalized with `filepath.Clean`, so `./report.pdf` and `report.pdf` map to one record.
  - Storage is initialized right after the embedding-key check, then `documentsAtPath(store, path)` (exact `DocumentQuery` `path` filter) fetches existing records.
  - Existing + no `--force` → `printDuplicateNotice` prints the newest doc ID, status, path and the `--force` hint, then `return` (exit 0). This happens BEFORE parsing, so duplicates cost zero parse work and zero embedding calls.
  - Existing + `--force` → the existing record(s) are deleted only AFTER parse+chunk succeed (a parse failure never destroys already-indexed data), then the normal index flow runs. All same-path duplicates are removed, not just the newest.
- Storage bug found and fixed en route (`internal/storage/sqlite.go` `opDeleteDocument`): the modernc driver only honors `_pragma`/`_time_format`/`_time_integer_format`/`_dqs`/`_error_rc` DSN params, so `_journal=WAL&_busy_timeout=5000&_fk=1` were silently ignored. Foreign keys were OFF, `ON DELETE CASCADE` never fired, and `DeleteDocument` left orphaned chunks (with embeddings) that `GetAllChunks`/`SearchByKeyword` kept returning. It now deletes chunks and the document in one transaction (same pattern as `DeleteWikiIndex`).
- Verified end-to-end with a mock OpenAI-compatible embedding server: duplicate add prints the notice and exits 0 with zero embedding calls; `./`-prefixed path still skips; `--force` rebuild leaves exactly one document; total embedding texts served = 2 across 4 runs.
- Tests: `TestDeleteDocumentRemovesChunks` (storage; fails without the fix), `TestDocumentsAtPath` + `TestForceReplaceCleansSamePathDocuments` (cmd/go-rag, real SQLite storage).

## Next steps
- Databases may already hold orphan chunks from pre-fix deletions; `GetAllChunks`/`SearchByKeyword` have no JOIN to `documents`, so those legacy orphans still pollute results. Consider a read-side JOIN or a one-off cleanup.
- The DSN silently ignores `_journal=WAL` and `_busy_timeout=5000`; switching to `_pragma=journal_mode(WAL)` / `_pragma=busy_timeout(5000)` (and optionally `_pragma=foreign_keys(1)`) would make the documented behavior real, but is a runtime behavior change left out of scope.
- Known follow-up: `SQLiteStorage.SearchByKeyword` pre-filters with one `LIKE '%<query>%'` on the raw query, so a multi-word Chinese query ("机器学习 算法") returns an empty candidate set before BM25 runs. The hybrid path is unaffected (it loads all chunks).
- Consider `--json` for `get-chunk` and `wiki` subcommands if scripting demand appears.

## Active decisions
- Dedup reuses the existing `DocumentQuery` `path` exact filter instead of adding `GetDocumentByPath`: no interface/mock churn, and it returns ALL same-path records so `--force` cleans up legacy duplicates in one pass.
- `--force` deletion is deferred until after parse+chunk succeed — "delete then re-index" holds, but a failure cannot destroy indexed data.
- Only `filepath.Clean` normalization is applied (no TrimSpace, no absolute paths, no case folding); stored paths stay as the user typed them modulo cleaning.
- Skip is a success outcome: stdout notice + exit 0, matching the task's requirement.

## Previous work
- Search/document enrichment + `--json` output (task 9e96a0dc): `search` results now carry `document_name`/`document_path` (CLI-layer `GetDocument` per distinct ID, missing docs degrade to `(unknown)`); `search --json` prints `{query, count, results}` and `list --json` prints `{total, offset, count, documents}` (snake_case keys, empty sets as `[]`); `reorderArgs` gained a `booleanFlags` set so `search --json <query>` no longer swallows the query. Human-readable output otherwise unchanged.
- BM25 CJK tokenisation: contiguous CJK runs split into overlapping bigrams + unigrams, CJK punctuation separates tokens (`internal/retriever`, `tokenize_cjk_test.go`).
- Personal wiki subsystem (`go-rag wiki`), paginated/searchable `list`, SQLite with serialized writes.

## Active decisions (earlier tasks)
- Document lookup for search results stays in the CLI layer via `GetDocument` per distinct ID — top-k is small, avoids changing the `Storage` interface. Revisit with a single `GetDocuments(ids)`/join if top-k or N+1 concerns grow.
- JSON keys are snake_case and stable regardless of hit state (no `omitempty` on name/path); JSON output is opt-in per command (`--json`); human-readable output stays byte-for-byte unchanged apart from the two added document lines in `search`.

